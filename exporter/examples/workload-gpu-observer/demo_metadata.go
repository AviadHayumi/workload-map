// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type demoMetadata struct {
	mu                                                     sync.RWMutex
	namespace                                              string
	maxAge                                                 time.Duration
	round                                                  demoRound
	at                                                     time.Time
	valid                                                  bool
	info, started, ends, status, assignmentStatus, healthy *prometheus.Desc
}
type demoRound struct {
	Version     int              `json:"version"`
	ID          string           `json:"round_id"`
	Started     time.Time        `json:"started_at"`
	Ends        time.Time        `json:"ends_at"`
	Updated     time.Time        `json:"updated_at"`
	Status      string           `json:"status"`
	Assignments []demoAssignment `json:"assignments"`
}
type demoAssignment struct {
	Namespace string `json:"namespace"`
	UID       string `json:"workload_uid"`
	Scenario  string `json:"scenario"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
}

func newDemoMetadata(namespace string, maxAge time.Duration) *demoMetadata {
	return &demoMetadata{namespace: namespace, maxAge: maxAge,
		info:             prometheus.NewDesc("workload_gpu_demo_intent_info", "Desired simulation role from the round ConfigMap; not measured activity.", []string{"namespace", "workload_uid", "scenario", "round_id"}, nil),
		started:          prometheus.NewDesc("workload_gpu_demo_round_started_timestamp_seconds", "Desired scenario round start time.", []string{"namespace", "round_id"}, nil),
		ends:             prometheus.NewDesc("workload_gpu_demo_round_ends_timestamp_seconds", "Desired scenario round end time.", []string{"namespace", "round_id"}, nil),
		status:           prometheus.NewDesc("workload_gpu_demo_round_status", "Scenario application status, independent of measured GPU activity.", []string{"namespace", "round_id", "status"}, nil),
		assignmentStatus: prometheus.NewDesc("workload_gpu_demo_assignment_status", "Whether desired annotations were applied to this workload cohort; not measured GPU activity.", []string{"namespace", "workload_uid", "round_id", "status", "reason"}, nil),
		healthy:          prometheus.NewDesc("workload_gpu_demo_metadata_healthy", "One for fresh, valid, unexpired desired scenario metadata.", []string{"namespace"}, nil)}
}
func (d *demoMetadata) poll(ctx context.Context, client kubernetes.Interface, name string) {
	cm, err := client.CoreV1().ConfigMaps(d.namespace).Get(ctx, name, metav1.GetOptions{})
	body := ""
	if err == nil {
		body = cm.Data["state.json"]
	}
	d.update(body, time.Now())
}
func (d *demoMetadata) update(body string, now time.Time) {
	round := demoRound{}
	valid := len(body) <= 1<<20 && json.Unmarshal([]byte(body), &round) == nil && round.Version == 1 && round.ID != "" && len(round.ID) <= 128 && len(round.Assignments) > 0 && len(round.Assignments) <= 1000 && !round.Started.After(now.Add(5*time.Second)) && round.Ends.After(now) && round.Ends.After(round.Started) && !round.Updated.Before(round.Started) && !round.Updated.After(now.Add(5*time.Second))
	valid = valid && (round.Status == "applying" || round.Status == "applied" || round.Status == "partial")
	seen := make(map[string]bool)
	for _, assignment := range round.Assignments {
		valid = valid && assignment.Namespace == d.namespace && assignment.UID != "" && len(assignment.UID) <= 128 && !seen[assignment.UID] && (assignment.Scenario == "active" || assignment.Scenario == "quiet")
		valid = valid && (assignment.Status == "" || assignment.Status == "pending" || assignment.Status == "applying" || assignment.Status == "applied" || assignment.Status == "partial" || assignment.Status == "blocked") && (assignment.Reason == "" || assignment.Reason == "none" || assignment.Reason == "cohort_unavailable" || assignment.Reason == "patch_failed")
		seen[assignment.UID] = true
	}
	d.mu.Lock()
	d.round, d.at, d.valid = round, now, valid
	d.mu.Unlock()
}
func (d *demoMetadata) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{d.info, d.started, d.ends, d.status, d.assignmentStatus, d.healthy} {
		ch <- desc
	}
}
func (d *demoMetadata) Collect(ch chan<- prometheus.Metric) {
	d.mu.RLock()
	round, at, valid := d.round, d.at, d.valid
	d.mu.RUnlock()
	now := time.Now()
	valid = valid && now.Sub(at) <= d.maxAge && round.Ends.After(now)
	health := 0.0
	if valid {
		health = 1
		for _, assignment := range round.Assignments {
			ch <- prometheus.MustNewConstMetric(d.info, prometheus.GaugeValue, 1, assignment.Namespace, assignment.UID, assignment.Scenario, round.ID)
			status, reason := assignment.Status, assignment.Reason
			if status == "" {
				status = "pending"
			}
			if reason == "" {
				reason = "none"
			}
			ch <- prometheus.MustNewConstMetric(d.assignmentStatus, prometheus.GaugeValue, 1, assignment.Namespace, assignment.UID, round.ID, status, reason)
		}
		ch <- prometheus.MustNewConstMetric(d.started, prometheus.GaugeValue, float64(round.Started.Unix()), d.namespace, round.ID)
		ch <- prometheus.MustNewConstMetric(d.ends, prometheus.GaugeValue, float64(round.Ends.Unix()), d.namespace, round.ID)
		ch <- prometheus.MustNewConstMetric(d.status, prometheus.GaugeValue, 1, d.namespace, round.ID, round.Status)
	}
	ch <- prometheus.MustNewConstMetric(d.healthy, prometheus.GaugeValue, health, d.namespace)
}
