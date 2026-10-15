// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

var indexBundleGVK = schema.GroupVersionKind{Group: "index.test", Version: "v1", Kind: "Bundle"}
var indexWorkerGVK = schema.GroupVersionKind{Group: "index.test", Version: "v1", Kind: "Worker"}

func indexTestMapping(gvk schema.GroupVersionKind, scope meta.RESTScope) *meta.RESTMapping {
	return &meta.RESTMapping{GroupVersionKind: gvk, Resource: gvk.GroupVersion().WithResource(strings.ToLower(gvk.Kind) + "s"), Scope: scope}
}

func indexTestObject(name, namespace string, gvk schema.GroupVersionKind, owner *unstructured.Unstructured) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	object.SetName(name)
	object.SetNamespace(namespace)
	object.SetUID(types.UID(name))
	object.SetResourceVersion("1")
	if owner != nil {
		object.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(owner)})
	}
	return object
}

func indexTestOwner(object *unstructured.Unstructured) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Name: object.GetName(), UID: object.GetUID(), Controller: ptr.To(true)}
}

func indexTestPod(name, namespace string, owner *unstructured.Unstructured) *corev1.Pod {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name), ResourceVersion: "1"}}
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{indexTestOwner(owner)}
	}
	return pod
}

func indexTestDataset() Dataset {
	a := indexTestObject("a", "team", indexBundleGVK, nil)
	b := indexTestObject("b", "team", indexBundleGVK, nil)
	child := indexTestObject("child", "team", indexWorkerGVK, a)
	return Dataset{
		Namespace: "team", Objects: []*unstructured.Unstructured{a, b, child},
		Pods:               []*corev1.Pod{indexTestPod("pod-a", "team", child), indexTestPod("pod-b", "team", b)},
		PodResourceVersion: "list-1",
		Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
			podGVK: indexTestMapping(podGVK, meta.RESTScopeNamespace), indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace), indexWorkerGVK: indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace),
		},
	}
}

func indexTestCapture(t *testing.T, index *Index, ref ObjectRef, names ...string) *Capture {
	t.Helper()
	capture, err := index.Capture(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(capture.Pods))
	for _, pod := range capture.Pods {
		got = append(got, pod.Name)
	}
	want := append([]string{}, names...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Pod names = %v, want %v", got, want)
	}
	return capture
}

func TestIndexPublicationAndCopyIsolation(t *testing.T) {
	ctx := context.Background()
	index := NewIndex()
	dataset := indexTestDataset()
	root := reference(dataset.Objects[0])
	if _, err := index.Capture(ctx, root); !errors.Is(err, ErrNotSynced) {
		t.Fatal(err)
	}
	generation, err := index.Replace(ctx, dataset)
	if err != nil || generation != 1 {
		t.Fatalf("replace = %d, %v", generation, err)
	}
	capture := indexTestCapture(t, index, root, "pod-a")
	if capture.Observation.Source != "indexed" || capture.Observation.Generation != generation || capture.Observation.PodResourceVersion != "list-1" || capture.Observation.Started.After(capture.Observation.Completed) {
		t.Fatalf("observation = %+v", capture.Observation)
	}
	if len(capture.Objects) != 2 || len(capture.Mappings) != 3 {
		t.Fatalf("missing closure: %+v", capture)
	}
	podSeed, err := podObject(dataset.Pods[0])
	if err != nil {
		t.Fatal(err)
	}
	indexTestCapture(t, index, reference(podSeed), "pod-a")
	dataset.Objects[0].SetName("caller mutation")
	dataset.Pods[0].Labels = map[string]string{"changed": "true"}
	dataset.Mappings[indexBundleGVK].Resource.Resource = "changed"
	capture.Objects[0].SetName("capture mutation")
	capture.Pods[0].Name = "capture mutation"
	capture.Mappings[indexBundleGVK].Resource.Resource = "changed"
	object, err := index.Get(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	object.SetName("get mutation")
	list, err := index.ListPods(ctx, "team")
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	list.Items[0].Name = "list mutation"
	mapping, err := index.Mapping(ctx, indexBundleGVK)
	if err != nil {
		t.Fatal(err)
	}
	mapping.Resource.Resource = "mapping mutation"
	fresh := indexTestCapture(t, index, root, "pod-a")
	if fresh.Objects[0].GetName() != "a" || len(fresh.Pods[0].Labels) != 0 || fresh.Mappings[indexBundleGVK].Resource.Resource != "bundles" {
		t.Fatal("caller mutation escaped into the index")
	}
}

func TestIndexReparentingCASAndImmutableOldCapture(t *testing.T) {
	ctx := context.Background()
	index := NewIndex()
	dataset := indexTestDataset()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	old := indexTestCapture(t, index, reference(dataset.Objects[0]), "pod-a")
	child := dataset.Objects[2].DeepCopy()
	child.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(dataset.Objects[1])})
	generation, err = index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{child}})
	if err != nil || generation != 2 {
		t.Fatalf("apply = %d, %v", generation, err)
	}
	indexTestCapture(t, index, reference(dataset.Objects[0]))
	indexTestCapture(t, index, reference(dataset.Objects[1]), "pod-a", "pod-b")
	if len(old.Pods) != 1 || old.Pods[0].Name != "pod-a" || old.Observation.Generation != 1 {
		t.Fatal("old capture changed after publication")
	}
	if actual, err := index.Apply(ctx, generation-1, Changes{Deletes: []ObjectRef{reference(child)}}); !errors.Is(err, ErrGenerationConflict) || actual != generation {
		t.Fatalf("stale batch = %d, %v", actual, err)
	}
	indexTestCapture(t, index, reference(dataset.Objects[1]), "pod-a", "pod-b")
}

func TestIndexMissingOwnersRetirementAndNameReuse(t *testing.T) {
	ctx := context.Background()
	index := NewIndex()
	dataset := indexTestDataset()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	old := dataset.Objects[2]
	generation, err = index.Apply(ctx, generation, Changes{Deletes: []ObjectRef{reference(old)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range dataset.Objects[:2] {
		if _, err := index.Capture(ctx, reference(root)); !errors.Is(err, ErrIncompleteOwnership) || !errors.Is(err, ownership.ErrMissing) {
			t.Fatalf("missing owner silently accepted: %v", err)
		}
	}
	if _, err := index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{old}}); !errors.Is(err, ErrRetiredUID) {
		t.Fatalf("retired UID resurrected: %v", err)
	}
	replacement := old.DeepCopy()
	replacement.SetUID("replacement")
	generation, err = index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{replacement}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Capture(ctx, reference(dataset.Objects[0])); !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatalf("replacement satisfied old reference: %v", err)
	}
	pod := dataset.Pods[0].DeepCopy()
	pod.OwnerReferences = []metav1.OwnerReference{indexTestOwner(replacement)}
	object, err := podObject(pod)
	if err != nil {
		t.Fatal(err)
	}
	_, err = index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{object}, Deletes: []ObjectRef{reference(old)}})
	if err != nil {
		t.Fatal(err)
	}
	capture := indexTestCapture(t, index, reference(dataset.Objects[0]), "pod-a")
	if capture.Observation.PodResourceVersion != "" {
		t.Fatal("incremental Pod update retained a stale list resourceVersion")
	}
	if got, err := index.Get(ctx, reference(replacement)); err != nil || got.GetUID() != replacement.GetUID() {
		t.Fatalf("late tombstone deleted replacement: %+v, %v", got, err)
	}
	// Authoritative replacement can intentionally reset retirement history.
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	indexTestCapture(t, index, reference(old), "pod-a")
}

func TestIndexReplacementRetiresOldLifetimeWithoutExplicitDelete(t *testing.T) {
	ctx := context.Background()
	index := NewIndex()
	dataset := indexTestDataset()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	replacement := dataset.Objects[2].DeepCopy()
	replacement.SetUID("new-child")
	generation, err = index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{replacement}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{dataset.Objects[2]}}); !errors.Is(err, ErrRetiredUID) {
		t.Fatalf("displaced lifetime was not retired: %v", err)
	}
	if _, err := index.Get(ctx, reference(dataset.Objects[2])); !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatalf("old identity still readable: %v", err)
	}
}

func TestIndexVersionAliasesAndMappingRepair(t *testing.T) {
	ctx := context.Background()
	index := NewIndex()
	dataset := indexTestDataset()
	alias := dataset.Objects[0].DeepCopy()
	aliasGVK := indexBundleGVK
	aliasGVK.Version = "v2"
	alias.SetGroupVersionKind(aliasGVK)
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	child := dataset.Objects[2].DeepCopy()
	child.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(alias)})
	generation, err = index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{child}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Capture(ctx, reference(dataset.Objects[0])); !meta.IsNoMatchError(err) || !errors.Is(err, ErrIncompleteOwnership) {
		t.Fatalf("unmapped owner did not fail: %v", err)
	}
	generation, err = index.Apply(ctx, generation, Changes{
		Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{aliasGVK: indexTestMapping(aliasGVK, meta.RESTScopeNamespace)},
		Upserts:  []*unstructured.Unstructured{dataset.Objects[0], alias},
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := indexTestCapture(t, index, reference(alias), "pod-a")
	if len(capture.Objects) != 3 || capture.Mappings[aliasGVK] == nil {
		t.Fatal("served aliases missing from capture")
	}
	alias.SetResourceVersion("2")
	generation, err = index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{alias}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Get(ctx, reference(dataset.Objects[0])); !apierrors.IsNotFound(err) {
		t.Fatalf("stale alias retained after update: %v", err)
	}
	indexTestCapture(t, index, reference(alias), "pod-a")
	if _, err := index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{dataset.Objects[0], alias}}); !errors.Is(err, ErrStaleInput) {
		t.Fatalf("inconsistent alias resourceVersions accepted: %v", err)
	}
	v1 := dataset.Objects[0].DeepCopy()
	v1.SetResourceVersion("2")
	if _, err := index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{v1, alias}}); err != nil {
		t.Fatal(err)
	}
	indexTestCapture(t, index, reference(v1), "pod-a")
}

func TestIndexScopeAndClusterWorkloads(t *testing.T) {
	ctx := context.Background()
	clusterGVK := schema.GroupVersionKind{Group: "index.test", Version: "v1", Kind: "ClusterBundle"}
	root := indexTestObject("cluster", "", clusterGVK, nil)
	dataset := Dataset{
		Objects: []*unstructured.Unstructured{root}, Pods: []*corev1.Pod{indexTestPod("first", "one", root), indexTestPod("second", "two", root)},
		Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{podGVK: indexTestMapping(podGVK, meta.RESTScopeNamespace), clusterGVK: indexTestMapping(clusterGVK, meta.RESTScopeRoot)},
	}
	index := NewIndex()
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	capture := indexTestCapture(t, index, reference(root), "first", "second")
	if capture.Observation.Namespace != "" {
		t.Fatal("cluster coverage must span all namespaces")
	}
	list, err := index.ListPods(ctx, "one")
	if err != nil || len(list.Items) != 1 || list.Items[0].Name != "first" {
		t.Fatalf("namespace list = %+v, %v", list, err)
	}
	dataset.Namespace = "one"
	dataset.Pods = dataset.Pods[:1]
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Capture(ctx, reference(root)); !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("scoped cluster capture = %v", err)
	}
	if _, err := index.ListPods(ctx, ""); !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("all-namespace list from scoped index = %v", err)
	}
	foreign := indexTestPod("outside", "two", root)
	dataset.Pods = append(dataset.Pods, foreign)
	if _, err := index.Replace(ctx, dataset); !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("outside Pod accepted: %v", err)
	}
	// A cluster-scoped dependent cannot own a reference to a namespaced object.
	parent := indexTestObject("parent", "one", indexBundleGVK, nil)
	root.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(parent)})
	dataset.Objects = []*unstructured.Unstructured{root, parent}
	dataset.Pods = nil
	dataset.Mappings[indexBundleGVK] = indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace)
	if _, err := index.Replace(ctx, dataset); !errors.Is(err, ownership.ErrScope) {
		t.Fatalf("invalid cluster->namespace edge accepted: %v", err)
	}
}

func TestIndexInvalidationCancellationAndRejectedBatchesAreAtomic(t *testing.T) {
	ctx := context.Background()
	index := NewIndex()
	dataset := indexTestDataset()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	bad := dataset.Objects[2].DeepCopy()
	bad.SetUID("")
	if _, err := index.Apply(ctx, generation, Changes{Deletes: []ObjectRef{reference(dataset.Objects[0])}, Upserts: []*unstructured.Unstructured{bad}}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	indexTestCapture(t, index, reference(dataset.Objects[0]), "pod-a")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := index.Apply(canceled, generation, Changes{Deletes: []ObjectRef{reference(dataset.Objects[0])}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := index.Replace(canceled, Dataset{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := index.Capture(canceled, reference(dataset.Objects[0])); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cause := errors.New("watch coverage lost")
	index.Invalidate(cause)
	if _, err := index.Capture(ctx, reference(dataset.Objects[0])); !errors.Is(err, ErrNotSynced) || !errors.Is(err, cause) {
		t.Fatalf("invalidated capture = %v", err)
	}
	if _, err := index.Apply(ctx, generation, Changes{}); !errors.Is(err, ErrNotSynced) {
		t.Fatalf("apply revived invalid index: %v", err)
	}
	if _, err := index.Get(ctx, reference(dataset.Objects[0])); !errors.Is(err, ErrNotSynced) {
		t.Fatal(err)
	}
	if _, err := index.Mapping(ctx, podGVK); !errors.Is(err, ErrNotSynced) {
		t.Fatal(err)
	}
	if _, err := index.ListPods(ctx, "team"); !errors.Is(err, ErrNotSynced) {
		t.Fatal(err)
	}
	newGeneration, err := index.Replace(ctx, dataset)
	if err != nil || newGeneration <= generation {
		t.Fatalf("resync = %d, %v", newGeneration, err)
	}
	indexTestCapture(t, index, reference(dataset.Objects[0]), "pod-a")
}

func TestIndexValidationAndCycleRecovery(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		change func(*Dataset)
		want   error
	}{
		{"missing mapping", func(d *Dataset) { delete(d.Mappings, indexWorkerGVK) }, ErrInvalidInput},
		{"wrong mapping", func(d *Dataset) { d.Mappings[indexWorkerGVK].GroupVersionKind = indexBundleGVK }, ErrInvalidInput},
		{"duplicate object", func(d *Dataset) { d.Objects = append(d.Objects, d.Objects[0]) }, ErrInvalidInput},
		{"duplicate Pod", func(d *Dataset) { d.Pods = append(d.Pods, d.Pods[0]) }, ErrInvalidInput},
		{"missing owner", func(d *Dataset) { d.Objects = d.Objects[:2] }, ownership.ErrMissing},
		{"cycle", func(d *Dataset) {
			d.Objects[0].SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(d.Objects[2])})
		}, ownership.ErrCycle},
		{"multiple controllers", func(d *Dataset) {
			d.Objects[2].SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(d.Objects[0]), indexTestOwner(d.Objects[1])})
		}, ownership.ErrMultipleControllers},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataset := indexTestDataset()
			test.change(&dataset)
			index := NewIndex()
			if _, err := index.Replace(ctx, dataset); !errors.Is(err, test.want) {
				t.Fatalf("replace = %v, want %v", err, test.want)
			}
			if _, err := index.ListPods(ctx, "team"); !errors.Is(err, ErrNotSynced) {
				t.Fatalf("invalid Replace published state: %v", err)
			}
		})
	}
	index := NewIndex()
	dataset := indexTestDataset()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	cycle := dataset.Objects[0].DeepCopy()
	cycle.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(dataset.Objects[2])})
	generation, err = index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{cycle}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Capture(ctx, reference(dataset.Objects[1])); !errors.Is(err, ownership.ErrCycle) {
		t.Fatalf("cycle failed to invalidate coverage: %v", err)
	}
	if _, err := index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{dataset.Objects[0]}}); err != nil {
		t.Fatal(err)
	}
	indexTestCapture(t, index, reference(dataset.Objects[0]), "pod-a")
}

func TestIndexCompletenessIsScopedAndErrorsAreDetached(t *testing.T) {
	ctx := context.Background()
	dataset := indexTestDataset()
	dataset.Namespace = ""
	other := indexTestObject("other", "elsewhere", indexBundleGVK, nil)
	dataset.Objects = append(dataset.Objects, other)
	outsider := indexTestPod("outsider", "elsewhere", other)
	dataset.Pods = append(dataset.Pods, outsider)
	index := NewIndex()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	missing := indexTestObject("missing", "elsewhere", indexBundleGVK, nil)
	outsider.OwnerReferences = []metav1.OwnerReference{indexTestOwner(missing)}
	object, err := podObject(outsider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{object}}); err != nil {
		t.Fatal(err)
	}
	indexTestCapture(t, index, reference(dataset.Objects[0]), "pod-a")
	_, err = index.Capture(ctx, reference(other))
	var failure *ownership.Error
	if !errors.As(err, &failure) || !errors.Is(err, ErrIncompleteOwnership) {
		t.Fatal(err)
	}
	failure.Expected.Name = "mutated"
	failure.Path[0].Name = "mutated"
	_, err = index.Capture(ctx, reference(other))
	if !errors.As(err, &failure) || failure.Expected.Name != "missing" || failure.Path[0].Name != "outsider" {
		t.Fatalf("caller mutated cached diagnostics: %v", err)
	}
}

func TestIndexRejectedReplaceKeepsPreviousGeneration(t *testing.T) {
	ctx := context.Background()
	index := NewIndex()
	dataset := indexTestDataset()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	invalid := indexTestDataset()
	invalid.Objects = invalid.Objects[:2]
	if _, err := index.Replace(ctx, invalid); !errors.Is(err, ErrIncompleteOwnership) {
		t.Fatalf("invalid replacement accepted: %v", err)
	}
	capture := indexTestCapture(t, index, reference(dataset.Objects[0]), "pod-a")
	if capture.Observation.Generation != generation {
		t.Fatal("rejected replacement changed generation")
	}
}

func TestIndexRootPodAndEmptyWorkload(t *testing.T) {
	ctx := context.Background()
	dataset := indexTestDataset()
	standalone := indexTestPod("standalone", "team", nil)
	standalone.Status.Phase = corev1.PodSucceeded
	dataset.Pods = []*corev1.Pod{standalone}
	index := NewIndex()
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	object, err := podObject(standalone)
	if err != nil {
		t.Fatal(err)
	}
	capture := indexTestCapture(t, index, reference(object), "standalone")
	if len(capture.Objects) != 0 || capture.Pods[0].Status.Phase != corev1.PodSucceeded {
		t.Fatal("standalone completed Pod was lost")
	}
	indexTestCapture(t, index, reference(dataset.Objects[0]))
}

func TestIndexConcurrentCaptureUsesOneGeneration(t *testing.T) {
	ctx := context.Background()
	index := NewIndex()
	dataset := indexTestDataset()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for n := 0; n < 200; n++ {
			child := dataset.Objects[2].DeepCopy()
			child.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(dataset.Objects[(n+1)%2])})
			var err error
			generation, err = index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{child}})
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for n := 0; n < 500; n++ {
		capture, err := index.Capture(ctx, reference(dataset.Objects[0]))
		if err != nil {
			t.Fatal(err)
		}
		want := int(capture.Observation.Generation % 2)
		if len(capture.Pods) != want {
			t.Errorf("generation %d contains %d Pods, want %d", capture.Observation.Generation, len(capture.Pods), want)
			break
		}
	}
	workers.Wait()
}

func BenchmarkIndexApplyOnePod(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			dataset := indexTestDataset()
			dataset.Pods = nil
			for n := 0; n < count; n++ {
				dataset.Pods = append(dataset.Pods, indexTestPod(fmt.Sprintf("pod-%d", n), "team", dataset.Objects[0]))
			}
			index := NewIndex()
			generation, err := index.Replace(context.Background(), dataset)
			if err != nil {
				b.Fatal(err)
			}
			object, err := podObject(dataset.Pods[0])
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				generation, err = index.Apply(context.Background(), generation, Changes{Upserts: []*unstructured.Unstructured{object}})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
