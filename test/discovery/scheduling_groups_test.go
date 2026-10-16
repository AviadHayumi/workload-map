// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build live_discovery

package discovery_test

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	example "github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func TestLiveLeaderWorkerSchedulingGroups(t *testing.T) {
	ctx, _, client, _, reader := liveClient(t)
	path := os.Getenv("KARTA_DISCOVERY_ORACLE")
	if path == "" {
		t.Fatal("set KARTA_DISCOVERY_ORACLE to the independent fixture UID oracle")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var expected oracle
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	var root identity
	var allUIDs []string
	wantGroups := make(map[string][]string)
	for _, workload := range expected.Workloads {
		if workload.Root.Kind != "LeaderWorkerSet" {
			continue
		}
		root, allUIDs = workload.Root, workload.PodUIDs
		for _, component := range workload.SemanticComponents {
			if component.Name != "group" {
				continue
			}
			for _, instance := range component.Instances {
				for _, replica := range instance.Replicas {
					wantGroups[replica.Key] = replica.PodUIDs
				}
			}
		}
	}
	if root.UID == "" || len(allUIDs) != 4 || len(wantGroups) != 2 {
		t.Fatal("oracle must contain two LeaderWorkerSet groups with four Pods")
	}
	apiPods, err := consumerE2EAPIPods(ctx, client, expected.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	var expectedBodies []*corev1.Pod
	for i := range apiPods.Items {
		if slices.Contains(allUIDs, string(apiPods.Items[i].UID)) {
			expectedBodies = append(expectedBodies, apiPods.Items[i].DeepCopy())
		}
	}
	assertPods(t, expectedBodies, allUIDs)
	dataset, err := sdk.Collect(ctx, reader, expected.Namespace, root.ref())
	if err != nil {
		t.Fatal(err)
	}
	index := sdk.NewIndex()
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []struct {
		name    string
		reader  sdk.Reader
		options sdk.Options
	}{
		{name: "direct", reader: reader},
		{name: "indexed", reader: index},
		{name: "indexed-attribution-cache", reader: index, options: sdk.Options{
			Cache: &sdk.CacheOptions{MaxWorkloads: 10, MaxPods: 100},
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			discoverer, err := karta.NewDiscoverer(mode.reader, mode.options)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				observed, err := discoverer.Workload(ctx, root.ref())
				if err != nil {
					t.Fatal(err)
				}
				captured := observed.Pods()
				assertPods(t, captured, allUIDs)
				consumerE2EPods(t, captured, expectedBodies)
				groups, err := example.SchedulingGroups(ctx, observed)
				if err != nil {
					t.Fatal(err)
				}
				if len(groups) != 2 {
					t.Fatalf("got %d gangs; leader and worker must share each group", len(groups))
				}
				var grouped []*corev1.Pod
				seen := make(map[string]bool)
				for _, group := range groups {
					if group.Name != "group" || len(group.Keys) != 2 || group.Keys[0] != root.Name {
						t.Fatalf("unexpected gang identity %q %v", group.Name, group.Keys)
					}
					key := group.Keys[1]
					want, ok := wantGroups[key]
					if !ok || seen[key] || len(want) != 2 {
						t.Fatalf("unexpected or repeated group %q", key)
					}
					seen[key] = true
					assertPods(t, group.Pods, want)
					roles := make(map[string]bool)
					for _, pod := range group.Pods {
						if pod.Labels["leaderworkerset.sigs.k8s.io/group-index"] != key {
							t.Fatal("gang crosses an operator group boundary")
						}
						roles[pod.Labels["leaderworkerset.sigs.k8s.io/worker-index"]] = true
						row := slices.IndexFunc(captured, func(candidate *corev1.Pod) bool { return candidate.UID == pod.UID })
						if row < 0 || !reflect.DeepEqual(pod, captured[row]) {
							t.Fatal("gang result did not retain the complete captured Pod")
						}
					}
					if len(roles) != 2 || !roles["0"] || !roles["1"] {
						t.Fatal("each gang must contain its real leader and worker")
					}
					grouped = append(grouped, group.Pods...)
				}
				assertPods(t, grouped, allUIDs)
				groups[0].Keys[0] = "changed"
				groups[0].Pods[0].Labels["example.org/changed"] = "true"
				groups[0].Pods[0].Spec.Containers[0].Image = "example/changed:v1"
				groups[0].Pods[0].Status.Phase = corev1.PodUnknown
				if !reflect.DeepEqual(observed.Pods(), captured) {
					t.Fatal("editing a gang result changed the captured workload")
				}
			}
		})
	}
}
