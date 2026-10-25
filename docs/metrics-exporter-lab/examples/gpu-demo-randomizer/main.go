// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type runner struct {
	client                                 kubernetes.Interface
	namespace, endpoint, configMap, holder string
}

type identity struct{ Namespace, Workload, UID string }

type podRef struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type assignment struct {
	Namespace   string   `json:"namespace"`
	Workload    string   `json:"workload"`
	WorkloadUID string   `json:"workload_uid"`
	Scenario    string   `json:"scenario"`
	Utilization string   `json:"utilization"`
	Pods        []podRef `json:"pods"`
	Applied     []string `json:"applied_pod_uids"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitempty"`
}

type round struct {
	Version     int          `json:"version"`
	ID          string       `json:"round_id"`
	Started     time.Time    `json:"started_at"`
	Ends        time.Time    `json:"ends_at"`
	Seed        uint64       `json:"seed"`
	Updated     time.Time    `json:"updated_at"`
	Status      string       `json:"status"`
	Assignments []assignment `json:"assignments"`
	LeaseHolder string       `json:"lease_holder,omitempty"`
	LeaseUntil  time.Time    `json:"lease_until,omitempty"`
}

type cohort struct {
	root identity
	pods []corev1.Pod
}

const (
	optIn         = "karta.demo/gpu-randomize"
	utilization   = "run.ai/simulated-gpu-utilization"
	roundDuration = 10 * time.Minute
	maxPods       = 512
	maxWorkloads  = 32
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	r := runner{}
	flag.StringVar(&r.namespace, "namespace", "", "Single opted-in demo namespace")
	flag.StringVar(&r.endpoint, "karta-url", "", "Direct Karta exporter /metrics URL")
	flag.StringVar(&r.configMap, "state-configmap", "karta-gpu-demo-round", "Precreated state ConfigMap")
	flag.Parse()
	u, err := url.Parse(r.endpoint)
	if r.namespace == "" || r.configMap == "" || err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("namespace, state-configmap and HTTP(S) karta-url without credentials are required")
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	cfg.Timeout = 5 * time.Second
	r.client, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	r.holder = os.Getenv("POD_UID")
	if r.holder == "" {
		return errors.New("POD_UID downward API environment variable is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return r.reconcile(ctx, time.Now().UTC())
}

func fetch(ctx context.Context, endpoint string) (map[string]identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("karta source status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 8<<20 {
		return nil, errors.New("karta snapshot exceeds 8 MiB")
	}
	return parseMappings(string(body))
}

func parseMappings(body string) (map[string]identity, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	fs, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if fs["karta_pod_workload_info"] == nil || fs["karta_exporter_observation_healthy"] == nil {
		return nil, errors.New("karta identity or health family missing")
	}
	healthy := map[string]bool{}
	bad := map[string]bool{}
	for _, m := range fs["karta_exporter_observation_healthy"].Metric {
		l := map[string]string{}
		for _, v := range m.Label {
			l[v.GetName()] = v.GetValue()
		}
		if l["scope"] != "workload" {
			continue
		}
		uid := l["workload_uid"]
		if m.GetGauge().GetValue() == 1 && l["reason"] == "none" {
			healthy[uid] = true
		} else {
			bad[uid] = true
		}
	}
	result := map[string]identity{}
	conflict := map[string]bool{}
	for _, m := range fs["karta_pod_workload_info"].Metric {
		l := map[string]string{}
		for _, v := range m.Label {
			l[v.GetName()] = v.GetValue()
		}
		uid := l["pod_uid"]
		root := identity{l["namespace"], l["workload"], l["workload_uid"]}
		if uid == "" || root.UID == "" || root.Workload == "" || l["owner_resolution"] != "resolved" || m.GetGauge().GetValue() != 1 || !healthy[root.UID] || bad[root.UID] {
			conflict[uid] = true
			continue
		}
		if old, ok := result[uid]; ok && old != root {
			conflict[uid] = true
		}
		result[uid] = root
	}
	for uid := range conflict {
		delete(result, uid)
	}
	return result, nil
}

func selectCohorts(pods []corev1.Pod, mapping map[string]identity, namespace string) (map[string]cohort, error) {
	if len(pods) > maxPods {
		return nil, errors.New("opted-in Pod limit exceeded")
	}
	result := map[string]cohort{}
	blocked := map[string]bool{}
	for _, p := range pods {
		if p.Namespace != namespace || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		gpu := int64(0)
		for _, c := range p.Spec.Containers {
			q := c.Resources.Limits["nvidia.com/gpu"]
			gpu += q.Value()
		}
		if gpu <= 0 {
			continue
		}
		id, ok := mapping[string(p.UID)]
		if !ok || id.Namespace != namespace {
			if p.Labels[optIn] == "true" {
				return nil, fmt.Errorf("opted-in GPU Pod %s lacks healthy unique Karta identity", p.Name)
			}
			continue
		}
		if p.Labels[optIn] != "true" || p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
			blocked[id.UID] = true
			continue
		}
		c := result[id.UID]
		c.root = id
		c.pods = append(c.pods, p)
		result[id.UID] = c
	}
	for uid := range blocked {
		delete(result, uid)
	}
	if len(result) > maxWorkloads {
		return nil, errors.New("opted-in workload limit exceeded")
	}
	return result, nil
}

func newRound(cohorts map[string]cohort, namespace string, now time.Time) round {
	// The initial round begins at the actual first reconciliation, preserving a full window.
	sum := sha256.Sum256([]byte(namespace + "/" + now.Format(time.RFC3339Nano)))
	seed := binary.LittleEndian.Uint64(sum[:8])
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	keys := slices.Sorted(maps.Keys(cohorts))
	rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	s := round{Version: 1, ID: fmt.Sprintf("%d", now.Unix()), Started: now, Ends: now.Add(roundDuration), Seed: seed, Status: "applying"}
	for i, uid := range keys {
		c := cohorts[uid]
		a := assignment{Namespace: namespace, Workload: c.root.Workload, WorkloadUID: uid, Scenario: "active", Utilization: "60-95"}
		if i%2 == 0 {
			a.Scenario = "quiet"
			a.Utilization = "0-3"
		}
		s.Assignments = append(s.Assignments, a)
	}
	return s
}

func (r runner) reconcile(ctx context.Context, now time.Time) error {
	mappings, err := fetch(ctx, r.endpoint)
	if err != nil {
		return fmt.Errorf("read Karta: %w", err)
	}
	list, err := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{Limit: maxPods + 1})
	if err != nil {
		return err
	}
	if list.Continue != "" {
		return errors.New("opted-in Pod listing exceeds bound")
	}
	cohorts, err := selectCohorts(list.Items, mappings, r.namespace)
	if err != nil {
		return err
	}
	if len(cohorts) == 0 {
		return errors.New("no complete opted-in GPU workloads are ready")
	}
	cm, err := r.client.CoreV1().ConfigMaps(r.namespace).Get(ctx, r.configMap, metav1.GetOptions{})
	if err != nil {
		return err
	}
	s := round{}
	if body := cm.Data["state.json"]; body != "" {
		if err = json.Unmarshal([]byte(body), &s); err != nil {
			return fmt.Errorf("decode existing round: %w", err)
		}
	}
	if s.LeaseHolder != "" && s.LeaseUntil.After(now) {
		log.Print("another demo reconciliation holds the state lease")
		return nil
	}
	if s.Version != 0 && s.Version != 1 {
		return errors.New("unsupported state version")
	}
	if s.Version == 0 || !s.Ends.After(now) {
		s = newRound(cohorts, r.namespace, now)
	}
	s.LeaseHolder = r.holder
	s.LeaseUntil = now.Add(2 * time.Minute)
	s.Updated = now
	s.Status = "applying"
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	cm.Data["state.json"] = string(body)
	cm, err = r.client.CoreV1().ConfigMaps(r.namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("claim round lease: %w", err)
	}
	var patchErrors []error
	for i := range s.Assignments {
		a := &s.Assignments[i]
		a.Pods = nil
		a.Applied = nil
		a.Status = "applying"
		a.Reason = ""
		c, ok := cohorts[a.WorkloadUID]
		if !ok {
			a.Status = "blocked"
			a.Reason = "cohort_unavailable"
			patchErrors = append(patchErrors, fmt.Errorf("workload %s has no complete eligible cohort", a.WorkloadUID))
			continue
		}
		for _, p := range c.pods {
			a.Pods = append(a.Pods, podRef{p.Name, string(p.UID)})
			patch, patchErr := podPatch(p, *a, s)
			if patchErr == nil && patch != nil {
				_, patchErr = r.client.CoreV1().Pods(r.namespace).Patch(ctx, p.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
			}
			if patchErr != nil {
				patchErrors = append(patchErrors, fmt.Errorf("patch %s/%s UID %s: %w", p.Namespace, p.Name, p.UID, patchErr))
				continue
			}
			a.Applied = append(a.Applied, string(p.UID))
		}
		a.Status = "applied"
		if len(a.Applied) != len(a.Pods) {
			a.Status = "partial"
			a.Reason = "patch_failed"
		}
		audit, _ := json.Marshal(struct {
			RoundID    string     `json:"round_id"`
			Seed       uint64     `json:"seed"`
			Assignment assignment `json:"assignment"`
		}{s.ID, s.Seed, *a})
		log.Print(string(audit))
	}
	s.Status = "applied"
	if len(patchErrors) > 0 {
		s.Status = "partial"
	}
	s.Updated = time.Now().UTC()
	s.LeaseHolder = ""
	s.LeaseUntil = time.Time{}
	body, err = json.Marshal(s)
	if err != nil {
		return err
	}
	cm.Data["state.json"] = string(body)
	if _, err = r.client.CoreV1().ConfigMaps(r.namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("publish round result: %w", err)
	}
	return errors.Join(patchErrors...)
}

func podPatch(p corev1.Pod, a assignment, s round) ([]byte, error) {
	if p.Labels[optIn] != "true" || p.Namespace != a.Namespace || p.UID == "" || p.ResourceVersion == "" {
		return nil, errors.New("pod is not an opted-in current object")
	}
	if (a.Scenario != "quiet" || a.Utilization != "0-3") && (a.Scenario != "active" || a.Utilization != "60-95") {
		return nil, errors.New("unsupported synthetic scenario")
	}
	annotations := maps.Clone(p.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[utilization] = a.Utilization
	annotations["karta.demo/round-id"] = s.ID
	annotations["karta.demo/workload-uid"] = a.WorkloadUID
	annotations["karta.demo/scenario"] = a.Scenario
	annotations["karta.demo/round-started-at"] = s.Started.Format(time.RFC3339)
	if maps.Equal(annotations, p.Annotations) {
		return nil, nil
	}
	return json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(p.UID)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": p.ResourceVersion},
		{"op": "test", "path": "/metadata/labels/karta.demo~1gpu-randomize", "value": "true"},
		{"op": "add", "path": "/metadata/annotations", "value": annotations},
	})
}
