// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package scale supplies synthetic workloads and an independent exposition oracle.
package scale

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

type Case struct {
	Workloads int
	Pods      int
}

type Fixture struct {
	Store      *store.Store
	Case       Case
	Generation []int
	Active     []bool
}

func (c Case) Name() string {
	return fmt.Sprintf("workloads_%d/pods_%d", c.Workloads, c.Pods)
}

// Cases requires explicit workload selection so ordinary test runs stay small.
func Cases(workloads, pods string) ([]Case, error) {
	if workloads == "all" {
		workloads = "1000,10000,100000"
	}
	if pods == "" || pods == "all" {
		pods = "0,1,10"
	}
	var result []Case
	for _, w := range strings.Split(workloads, ",") {
		n, err := strconv.Atoi(w)
		if err != nil || (n != 1000 && n != 10000 && n != 100000) {
			return nil, fmt.Errorf("workloads must be 1000, 10000, 100000, or a comma-separated selection: %q", w)
		}
		for _, p := range strings.Split(pods, ",") {
			count, err := strconv.Atoi(p)
			if err != nil || (count != 0 && count != 1 && count != 10) {
				return nil, fmt.Errorf("pods per workload must be 0, 1, or 10: %q", p)
			}
			result = append(result, Case{Workloads: n, Pods: count})
		}
	}
	return result, nil
}

func New(c Case) *Fixture {
	f := &Fixture{Store: store.New(), Case: c, Generation: make([]int, c.Workloads), Active: make([]bool, c.Workloads)}
	for i := range c.Workloads {
		f.Active[i] = true
		f.insert(i)
	}
	f.PublishHealth()
	return f
}

func (f *Fixture) Workload(i int) store.WorkloadRecord {
	phase := v1alpha1.RunningStatus
	if (i+f.Generation[i])%2 != 0 {
		phase = v1alpha1.FailedStatus
	}
	return store.WorkloadRecord{
		UID:   types.UID(fmt.Sprintf("workload-%d-v%d", i, f.Generation[i])),
		Ref:   store.WorkloadRef{Namespace: "scale", Name: fmt.Sprintf("workload-%d", i), Group: "batch", Version: "v1", Kind: "Job"},
		Karta: "scale-job", KartaSource: "cluster", OwnerResolution: "resolved",
		CreatedTimestamp: 1792324800, HasStatus: true, Phases: []v1alpha1.ResourceStatus{phase},
	}
}

func (f *Fixture) insert(i int) {
	w := f.Workload(i)
	evidence := store.Evidence{ExpiresAt: time.Now().Add(time.Hour)}
	rows := make([]store.PublishedPod, f.Case.Pods)
	for p := range f.Case.Pods {
		name := fmt.Sprintf("pod-%d-%d", i, p)
		rows[p] = store.PublishedPod{Record: store.PodRecord{
			UID: types.UID(fmt.Sprintf("%s-v%d", name, f.Generation[i])), Namespace: "scale", Name: name,
			WorkloadUID: w.UID, Workload: w.Ref, OwnerResolution: "resolved",
			Component: "worker", Instance: fmt.Sprintf("instance-%d", p%2), Replica: strconv.Itoa(p),
		}, Mapping: evidence}
	}
	f.Store.Publish(&store.Publication{Workload: w, Status: evidence, Creation: evidence, Membership: evidence, PodPages: store.BuildPodPages(rows, nil)})
}

func (f *Fixture) Delete(i int) {
	f.Store.RemovePublication(f.Workload(i).UID)
	f.Active[i] = false
}

func (f *Fixture) Replace(i int) {
	f.Delete(i)
	f.Generation[i]++
	f.Active[i] = true
	f.insert(i)
}

func (f *Fixture) PublishHealth() {
	f.Store.SetPublicationHealth([]store.HealthRecord{{Scope: "resource_type", Ref: store.WorkloadRef{Namespace: "scale", Group: "batch", Version: "v1", Kind: "Job"}, Healthy: true, VerifiedAt: time.Now(), MaxAge: time.Hour}})
}
