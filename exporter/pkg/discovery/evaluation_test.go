// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
)

func evaluationFixture(t *testing.T, definition *v1alpha1.Karta, body map[string]any, labels, annotations map[string]string) (*Discovery, *KartaWorkload, *corev1.Pod, *testReader) {
	t.Helper()
	inv := testDefinition(definition)
	root := testRef(inv[kartasKey(definition)].GVK, "root")
	object := &unstructured.Unstructured{Object: body}
	object.SetGroupVersionKind(root.GVK)
	object.SetName(root.Name)
	object.SetNamespace(root.Namespace)
	object.SetUID(root.UID)
	object.SetResourceVersion(root.ResourceVersion)
	reader := &testReader{objects: map[ObjectRef]*unstructured.Unstructured{root: object}}
	d := New(reader, inv)
	observe(d, root)
	podRef := testRef(podKind, "pod")
	observe(d, podRef, root)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podRef.Name, Namespace: podRef.Namespace, UID: podRef.UID, ResourceVersion: podRef.ResourceVersion, Labels: labels, Annotations: annotations}}
	return d, resolveTest(t, d, podRef).KartaWorkload(), pod, reader
}
func jobsetBody() map[string]any {
	return map[string]any{"spec": map[string]any{"replicatedJobs": []any{map[string]any{"name": "trainer"}}}, "status": map[string]any{"replicatedJobsStatus": []any{map[string]any{"ready": int64(1), "active": int64(1), "failed": int64(0)}}}}
}
func TestJobSetEvaluationAndInvalidInstances(t *testing.T) {
	for _, instance := range []string{"trainer", "", "undeclared"} {
		t.Run(instance, func(t *testing.T) {
			d, w, pod, _ := evaluationFixture(t, kartas.Jobset(), jobsetBody(), map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": instance, "jobset.sigs.k8s.io/job-index": "2"}, nil)
			batch := d.NewEvaluationBatch()
			mapping, err := w.PodMapping(t.Context(), pod, batch)
			if instance != "trainer" {
				if !errors.Is(err, ErrEvaluation) {
					t.Fatalf("mapping %+v: %v", mapping, err)
				}
			} else if err != nil || mapping.Component != "replicatedjob" || mapping.Instance != "trainer" || mapping.Group != "2" || !d.IsCurrent(mapping.State()) {
				t.Fatalf("mapping %+v: %v", mapping, err)
			}
			status, err := w.Status(t.Context(), batch)
			if err != nil || !status.Defined || !slices.Contains(status.Matched, v1alpha1.ResourceStatus("Running")) {
				t.Fatalf("status %+v: %v", status, err)
			}
		})
	}
}
func TestLWSInheritedReplicaGroup(t *testing.T) {
	for _, leader := range []bool{true, false} {
		t.Run(map[bool]string{true: "leader", false: "worker"}[leader], func(t *testing.T) {
			labels := map[string]string{"leaderworkerset.sigs.k8s.io/group-index": "3", "leaderworkerset.sigs.k8s.io/worker-index": "1"}
			annotations := map[string]string{"leaderworkerset.sigs.k8s.io/leader-name": "leader"}
			expected := "worker"
			if leader {
				labels["leaderworkerset.sigs.k8s.io/worker-index"] = "0"
				annotations = nil
				expected = "leader"
			}
			body := map[string]any{"status": map[string]any{"replicas": int64(2), "readyReplicas": int64(2), "updatedReplicas": int64(2)}}
			d, w, pod, _ := evaluationFixture(t, kartas.LWS(), body, labels, annotations)
			batch := d.NewEvaluationBatch()
			mapping, err := w.PodMapping(t.Context(), pod, batch)
			if err != nil || mapping.Component != expected || mapping.Group != "3" || mapping.Instance != "" {
				t.Fatalf("mapping %+v: %v", mapping, err)
			}
			status, err := w.Status(t.Context(), batch)
			if err != nil || !slices.Contains(status.Matched, v1alpha1.ResourceStatus("Running")) {
				t.Fatalf("status %+v: %v", status, err)
			}
		})
	}
}
func TestStatusFailureDoesNotPoisonMapping(t *testing.T) {
	definition := kartas.Jobset()
	definition.Spec.StructureDefinition.RootComponent.StatusDefinition.StatusMappings.Running = []v1alpha1.StatusMatcher{{ByExpression: &v1alpha1.ExpressionMatcher{Expression: `error("broken status")`, ExpectedResult: "true"}}}
	d, w, pod, _ := evaluationFixture(t, definition, jobsetBody(), map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "trainer", "jobset.sigs.k8s.io/job-index": "0"}, nil)
	batch := d.NewEvaluationBatch()
	if _, err := w.Status(t.Context(), batch); !errors.Is(err, ErrEvaluation) {
		t.Fatalf("got %v", err)
	}
	if _, err := w.PodMapping(t.Context(), pod, batch); err != nil {
		t.Fatal(err)
	}
}
func TestSelectedVersionRequiresFullCoverageAndSameUID(t *testing.T) {
	definition := kartas.Jobset()
	d, w, _, reader := evaluationFixture(t, definition, jobsetBody(), nil, nil)
	selected := w.root
	selected.GVK.Version = "v1beta1"
	definition.Spec.StructureDefinition.RootComponent.Kind.Version = selected.GVK.Version
	w.selection.GVK = selected.GVK
	w.selection.Definition = definition
	batch := d.NewEvaluationBatch()
	if _, err := w.Status(t.Context(), batch); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	object := reader.objects[w.root].DeepCopy()
	object.SetGroupVersionKind(selected.GVK)
	reader.objects[selected] = object
	o := observe(d, selected)
	d.CoverageChanged(CoverageChange{Key: o.WatchKey, Run: 1, Ready: true})
	w = resolveTest(t, d, w.root).KartaWorkload()
	w.selection.GVK = selected.GVK
	w.selection.Definition = definition
	if _, err := w.Status(t.Context(), d.NewEvaluationBatch()); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	d.CoverageChanged(CoverageChange{Key: o.WatchKey, Run: 1, Ready: true, FullObject: true})
	w = resolveTest(t, d, w.root).KartaWorkload()
	w.selection.GVK = selected.GVK
	w.selection.Definition = definition
	if _, err := w.Status(t.Context(), d.NewEvaluationBatch()); err != nil {
		t.Fatal(err)
	}
	object.SetUID("different-uid")
	reader.objects[selected] = object
	if _, err := w.Status(t.Context(), d.NewEvaluationBatch()); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}
func TestEvaluationTokensCancellationAndConcurrency(t *testing.T) {
	d, w, pod, _ := evaluationFixture(t, kartas.Jobset(), jobsetBody(), map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "trainer", "jobset.sigs.k8s.io/job-index": "0"}, nil)
	if d.IsCurrent() || d.IsCurrent(CapturedState{}) || New(nil, testInventory{}).IsCurrent(w.state) {
		t.Fatal("invalid token accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	batch := d.NewEvaluationBatch()
	if _, err := w.Status(ctx, batch); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := w.PodMapping(ctx, pod, batch); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := w.Status(t.Context(), batch); err != nil {
				t.Error(err)
			}
			if _, err := w.PodMapping(t.Context(), pod, batch); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	status, err := w.Status(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	d.DefinitionChanged(w.root.GVK.GroupKind())
	if d.IsCurrent(status.State()) {
		t.Fatal("definition revision did not invalidate status")
	}
	if _, err := w.Status(t.Context(), batch); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}
func TestMappingFailureDoesNotPoisonStatus(t *testing.T) {
	definition := kartas.Jobset()
	definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentInstanceSelector.IdPath = `error("broken mapping")`
	definition.Spec.StructureDefinition.ChildComponents[0].InstanceIdPath = ptr.To(".spec.replicatedJobs[].name")
	d, w, pod, _ := evaluationFixture(t, definition, jobsetBody(), nil, nil)
	batch := d.NewEvaluationBatch()
	if _, err := w.PodMapping(t.Context(), pod, batch); !errors.Is(err, ErrEvaluation) {
		t.Fatal(err)
	}
	if _, err := w.Status(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
}

func TestStatusUndefinedAndAbsent(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(map[bool]string{true: "absent", false: "no-match"}[absent], func(t *testing.T) {
			definition := kartas.Jobset()
			if absent {
				definition.Spec.StructureDefinition.RootComponent.StatusDefinition = nil
			}
			d, w, _, _ := evaluationFixture(t, definition, map[string]any{}, nil, nil)
			status, err := w.Status(t.Context(), d.NewEvaluationBatch())
			if err != nil {
				t.Fatal(err)
			}
			if absent {
				if status.Defined || len(status.Matched) != 0 {
					t.Fatalf("%+v", status)
				}
			} else if !status.Defined || !slices.Contains(status.Matched, v1alpha1.UndefinedStatus) {
				t.Fatalf("%+v", status)
			}
		})
	}
}
func TestRootReadVersionMismatch(t *testing.T) {
	d, w, _, r := evaluationFixture(t, kartas.Jobset(), jobsetBody(), nil, nil)
	r.objects[w.root].SetResourceVersion("2")
	if _, err := w.Status(t.Context(), d.NewEvaluationBatch()); !errors.Is(err, ErrStale) {
		t.Fatalf("got %v", err)
	}
}
func TestPodUpdateInvalidatesMapping(t *testing.T) {
	d, w, pod, _ := evaluationFixture(t, kartas.Jobset(), jobsetBody(), map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "trainer", "jobset.sigs.k8s.io/job-index": "0"}, nil)
	batch := d.NewEvaluationBatch()
	mapping, err := w.PodMapping(t.Context(), pod, batch)
	if err != nil {
		t.Fatal(err)
	}
	ref := testRef(podKind, "pod")
	ref.ResourceVersion = "2"
	observe(d, ref, w.root)
	if d.IsCurrent(mapping.State()) {
		t.Fatal("Pod update did not invalidate mapping")
	}
	if _, err := w.PodMapping(t.Context(), pod, batch); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	pod.ResourceVersion = "2"
	if _, err := w.PodMapping(t.Context(), pod, batch); err != nil {
		t.Fatal(err)
	}
}

type countingReader struct {
	CachedReader
	reads int
}

func (r *countingReader) Get(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	r.reads++
	return r.CachedReader.Get(ctx, ref)
}
func TestBatchSharesRootReadAndNewBatchRefreshes(t *testing.T) {
	d, w, pod, r := evaluationFixture(t, kartas.Jobset(), jobsetBody(), map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "trainer", "jobset.sigs.k8s.io/job-index": "0"}, nil)
	reader := &countingReader{CachedReader: r}
	d.reader = reader
	sibling := testRef(podKind, "sibling")
	observe(d, sibling, w.root)
	other := resolveTest(t, d, sibling).KartaWorkload()
	batch := d.NewEvaluationBatch()
	if _, err := w.Status(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Status(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PodMapping(t.Context(), pod, batch); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 1 {
		t.Fatalf("got %d root reads", reader.reads)
	}
	if _, err := other.Status(t.Context(), d.NewEvaluationBatch()); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 2 {
		t.Fatalf("new batch did not refresh: %d", reader.reads)
	}
}
