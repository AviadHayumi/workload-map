// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func TestRecordedWorkloadMutations(t *testing.T) {
	for _, cache := range []bool{false, true} {
		t.Run(fmt.Sprintf("cache=%v", cache), func(t *testing.T) {
			flow, _ := readRecordedFlow(t, "testdata/recordings/completed.yaml", "")
			index := sdk.NewIndex()
			options := sdk.Options{}
			if cache {
				options.Cache = &sdk.CacheOptions{MaxWorkloads: 2, MaxPods: 16}
			}
			discoverer, err := karta.NewDiscoverer(index, options)
			if err != nil {
				t.Fatal(err)
			}
			frames := 0
			for frame, event := range flow.Events {
				if event.Kind != "STATE" {
					continue
				}
				original := &unstructured.Unstructured{Object: event.Object}
				generation, err := index.Replace(t.Context(), recordingDataset(original))
				if err != nil {
					t.Fatal(err)
				}
				observed, err := discoverer.Workload(t.Context(), recordingRef(original))
				if err != nil {
					t.Fatal(err)
				}
				before := observed.Object()
				editor, err := karta.New(observed.Definition(), before)
				if err != nil {
					t.Fatal(err)
				}
				if err := editor.UpdatePodTemplate(t.Context(), "replicatedjob", karta.PodPatch{SchedulerName: ptr.To("replay-scheduler")}, karta.WithInstances("workers")); err != nil {
					t.Fatal(err)
				}
				expected := original.DeepCopy()
				jobs, _, err := unstructured.NestedSlice(expected.Object, "spec", "replicatedJobs")
				if err != nil {
					t.Fatal(err)
				}
				for _, job := range jobs {
					item := job.(map[string]any)
					if item["name"] == "workers" {
						if err := unstructured.SetNestedField(item, "replay-scheduler", "template", "spec", "template", "spec", "schedulerName"); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := unstructured.SetNestedSlice(expected.Object, jobs, "spec", "replicatedJobs"); err != nil {
					t.Fatal(err)
				}
				edited, err := editor.Object()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(expected, edited) {
					t.Fatalf("frame %d: template edit changed fields outside the explicit scheduler path", frame)
				}
				if !reflect.DeepEqual(before, observed.Object()) {
					t.Fatal("offline template edit changed captured workload")
				}
				for _, tc := range []struct {
					name  string
					apply func() error
				}{
					{"unsupported component fields", func() error {
						return editor.UpdatePodTemplate(t.Context(), "jobset", karta.PodPatch{SchedulerName: ptr.To("invalid")})
					}},
					{"unknown instance", func() error {
						return editor.UpdatePodTemplate(t.Context(), "replicatedjob", karta.PodPatch{SchedulerName: ptr.To("invalid")}, karta.WithInstances("absent"))
					}},
					{"invalid field", func() error {
						return editor.UpdatePodTemplate(t.Context(), "replicatedjob", karta.Patch{"spec": map[string]any{"typo": true}})
					}},
					{"canceled", func() error {
						ctx, cancel := context.WithCancel(t.Context())
						cancel()
						return editor.UpdatePodTemplate(ctx, "replicatedjob", karta.PodPatch{SchedulerName: ptr.To("invalid")})
					}},
				} {
					rejection := tc.apply()
					if rejection == nil {
						t.Fatalf("%s succeeded", tc.name)
					}
					if tc.name == "unsupported component fields" && !errors.Is(rejection, karta.ErrNotSupported) {
						t.Fatalf("unsupported error=%v", rejection)
					}
					after, err := editor.Object()
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(edited, after) {
						t.Fatalf("%s failed non-atomically", tc.name)
					}
				}
				for _, suspend := range []bool{true, false} {
					if suspend {
						err = editor.Suspend(t.Context())
					} else {
						err = editor.Resume(t.Context())
					}
					if err != nil {
						t.Fatal(err)
					}
					if err := unstructured.SetNestedField(expected.Object, suspend, "spec", "suspend"); err != nil {
						t.Fatal(err)
					}
					result, err := editor.Object()
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(expected, result) {
						t.Fatalf("suspend=%v changed unrelated fields", suspend)
					}
				}
				// Publishing the completed local edit is explicit. It must invalidate the
				// cached observation, while the earlier capture remains unchanged.
				generation, err = index.Apply(t.Context(), generation, sdk.Changes{Upserts: []*unstructured.Unstructured{expected}})
				if err != nil {
					t.Fatal(err)
				}
				refreshed, err := discoverer.Workload(t.Context(), recordingRef(expected))
				if err != nil {
					t.Fatal(err)
				}
				if refreshed.Observation().Generation != generation || !reflect.DeepEqual(expected, refreshed.Object()) || !reflect.DeepEqual(before, observed.Object()) {
					t.Fatal("publication did not preserve isolated observations")
				}
				beforeTree, err := observed.DesiredTree(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				afterTree, err := refreshed.DesiredTree(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(beforeTree.RootStatus, afterTree.RootStatus) {
					t.Fatal("offline spec mutation fabricated a controller status update")
				}
				frames++
			}
			if frames != 6 {
				t.Fatalf("completed recording frames=%d, want 6", frames)
			}
			t.Logf("%d recorded frames: exact targeted edit, 4 atomic rejections, suspend/resume, explicit index publication", frames)
		})
	}
}

// The recording contains only a root. These explicit descendant fixtures extend
// it with two named instances, two replica keys each, and two Pods per Job.
func TestRecordedRootGroupingAndUpdates(t *testing.T) {
	for _, cache := range []bool{false, true} {
		t.Run(fmt.Sprintf("cache=%v", cache), func(t *testing.T) {
			flow, _ := readRecordedFlow(t, "testdata/recordings/running.yaml", "")
			root := (&unstructured.Unstructured{Object: flow.Events[len(flow.Events)-1].Object}).DeepCopy()
			jobs, _, err := unstructured.NestedSlice(root.Object, "spec", "replicatedJobs")
			if err != nil {
				t.Fatal(err)
			}
			first := jobs[0].(map[string]any)
			first["replicas"] = int64(2)
			second := runtime.DeepCopyJSON(first)
			second["name"] = "chief"
			if err := unstructured.SetNestedSlice(root.Object, []any{first, second}, "spec", "replicatedJobs"); err != nil {
				t.Fatal(err)
			}
			dataset := recordingDataset(root)
			jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
			dataset.Mappings[jobGVK] = &meta.RESTMapping{GroupVersionKind: jobGVK, Resource: jobGVK.GroupVersion().WithResource("jobs"), Scope: meta.RESTScopeNamespace}
			expected := map[string][]*corev1.Pod{}
			for _, instance := range []string{"workers", "chief"} {
				for _, replica := range []string{"0", "1"} {
					name := instance + "-" + replica
					job := &unstructured.Unstructured{}
					job.SetGroupVersionKind(jobGVK)
					job.SetName(name)
					job.SetNamespace(root.GetNamespace())
					job.SetUID(types.UID("job-" + name))
					job.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: root.GetAPIVersion(), Kind: root.GetKind(), Name: root.GetName(), UID: root.GetUID(), Controller: ptr.To(true)}})
					dataset.Objects = append(dataset.Objects, job)
					for n := range 2 {
						podName := fmt.Sprintf("%s-%d", name, n)
						pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: root.GetNamespace(), UID: types.UID("pod-" + podName), Labels: map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": instance, "jobset.sigs.k8s.io/job-index": replica}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: job.GetUID(), Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "ghcr.io/example/worker:v1"}}}, Status: corev1.PodStatus{Phase: []corev1.PodPhase{corev1.PodRunning, corev1.PodSucceeded}[n]}}
						dataset.Pods = append(dataset.Pods, pod)
						expected[instance+"/"+replica] = append(expected[instance+"/"+replica], pod)
					}
				}
			}
			index := sdk.NewIndex()
			generation, err := index.Replace(t.Context(), dataset)
			if err != nil {
				t.Fatal(err)
			}
			options := sdk.Options{}
			if cache {
				options.Cache = &sdk.CacheOptions{MaxWorkloads: 2, MaxPods: 16}
			}
			discoverer, err := karta.NewDiscoverer(index, options)
			if err != nil {
				t.Fatal(err)
			}
			seed := dataset.Pods[0]
			seedRef := sdk.ObjectRef{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, Namespace: seed.Namespace, Name: seed.Name, UID: seed.UID}
			observed, err := discoverer.WorkloadForPod(t.Context(), seedRef)
			if err != nil {
				t.Fatal(err)
			}
			if observed.Root() != recordingRef(root) || len(observed.Pods()) != 8 {
				t.Fatal("Pod did not resolve to complete JobSet")
			}
			component, err := observed.Component("replicatedjob")
			if err != nil {
				t.Fatal(err)
			}
			for _, instance := range []string{"workers", "chief"} {
				instanceScope, err := component.Instance(instance)
				if err != nil {
					t.Fatal(err)
				}
				members, err := instanceScope.Pods()
				if err != nil || len(members) != 4 {
					t.Fatalf("instance %s: %d Pods, %v", instance, len(members), err)
				}
				for _, replica := range []string{"0", "1"} {
					group, err := instanceScope.Replica(replica)
					if err != nil {
						t.Fatal(err)
					}
					got, err := group.Pods()
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, expected[instance+"/"+replica]) {
						t.Fatalf("instance %s replica %s: full Pods differ", instance, replica)
					}
				}
			}
			// Relabel one Pod's instance and replica, with unchanged UID and RV. Cached
			// membership must follow current content, not only the old identity.
			moved := seed.DeepCopy()
			moved.Labels["jobset.sigs.k8s.io/replicatedjob-name"] = "chief"
			moved.Labels["jobset.sigs.k8s.io/job-index"] = "1"
			moved.Status.Phase = corev1.PodFailed
			body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(moved)
			if err != nil {
				t.Fatal(err)
			}
			generation, err = index.Apply(t.Context(), generation, sdk.Changes{Upserts: []*unstructured.Unstructured{{Object: body}}})
			if err != nil {
				t.Fatal(err)
			}
			refreshed, err := discoverer.WorkloadForPod(t.Context(), seedRef)
			if err != nil {
				t.Fatal(err)
			}
			changedComponent, err := refreshed.Component("replicatedjob")
			if err != nil {
				t.Fatal(err)
			}
			chief, err := changedComponent.Instance("chief")
			if err != nil {
				t.Fatal(err)
			}
			group, err := chief.Replica("1")
			if err != nil {
				t.Fatal(err)
			}
			changed, err := group.Pods()
			if err != nil {
				t.Fatal(err)
			}
			if len(changed) != 3 || refreshed.Observation().Generation != generation {
				t.Fatal("new group membership missing after update")
			}
			found := false
			for _, pod := range changed {
				if pod.UID == moved.UID {
					found = reflect.DeepEqual(pod, moved)
				}
			}
			if !found {
				t.Fatal("updated full Pod and phase missing from new group")
			}
			workers, err := changedComponent.Instance("workers")
			if err != nil {
				t.Fatal(err)
			}
			workersGroup, err := workers.Replica("0")
			if err != nil {
				t.Fatal(err)
			}
			remaining, err := workersGroup.Pods()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(remaining, expected["workers/0"][1:]) {
				t.Fatal("relabel left the moved Pod in its old group")
			}
			all := refreshed.Pods()
			if len(all) != 8 {
				t.Fatal("relabel changed total workload membership")
			}
			expectedBodies := map[types.UID]*corev1.Pod{}
			for _, pod := range dataset.Pods {
				expectedBodies[pod.UID] = pod
			}
			expectedBodies[moved.UID] = moved
			for _, pod := range all {
				if !reflect.DeepEqual(expectedBodies[pod.UID], pod) {
					t.Fatal("relabel changed an unrelated Pod or duplicated membership")
				}
				delete(expectedBodies, pod.UID)
			}
			if len(expectedBodies) != 0 {
				t.Fatal("relabel omitted a workload Pod")
			}
			oldWorkers, err := component.Instance("workers")
			if err != nil {
				t.Fatal(err)
			}
			oldGroup, err := oldWorkers.Replica("0")
			if err != nil {
				t.Fatal(err)
			}
			oldPods, err := oldGroup.Pods()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(oldPods, expected["workers/0"]) {
				t.Fatal("new publication changed old group")
			}
			t.Log("Pod -> JobSet -> component -> named instance -> replica returned exact full Pods; relabel and Pod phase update preserved old capture")
		})
	}
}
