// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"weak"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type attributionFixture struct {
	index      *Index
	resolver   *Resolver
	root       *unstructured.Unstructured
	pods       []*corev1.Pod
	definition *v1alpha1.Karta
	dataset    Dataset
	generation uint64
}

type wrappedCacheIndex struct{ *Index }

func attributionFixtureFor(t *testing.T, count int) *attributionFixture {
	t.Helper()
	f := &attributionFixture{index: NewIndex(), root: indexTestObject("root", "team", indexBundleGVK, nil)}
	f.root.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "worker", "image": "ghcr.io/example/worker:v1", "args": []any{"--fixture"}}}}
	f.root.Object["status"] = map[string]any{"phase": "Running"}
	for i := 0; i < count; i++ {
		pod := indexTestPod(fmt.Sprintf("pod-%02d", i), "team", f.root)
		pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
		pod.Labels = map[string]string{"role": "worker", "replica": "a"}
		pod.Spec.Containers = []corev1.Container{{Name: "worker", Image: "ghcr.io/example/worker:v1", Args: []string{"--fixture"}}}
		f.pods = append(f.pods, pod)
	}
	f.dataset = indexTestDataset()
	f.dataset.Objects = []*unstructured.Unstructured{f.root}
	f.dataset.Pods = f.pods
	var err error
	f.generation, err = f.index.Replace(t.Context(), f.dataset)
	if err != nil {
		t.Fatal(err)
	}
	f.definition = &v1alpha1.Karta{ObjectMeta: metav1.ObjectMeta{Name: "cache-test"}}
	f.definition.Spec.StructureDefinition.RootComponent = v1alpha1.ComponentDefinition{Name: "root", Kind: &v1alpha1.GroupVersionKind{Group: indexBundleGVK.Group, Version: indexBundleGVK.Version, Kind: indexBundleGVK.Kind}, StatusDefinition: &v1alpha1.StatusDefinition{}}
	f.definition.Spec.StructureDefinition.ChildComponents = []v1alpha1.ComponentDefinition{{Name: "worker", OwnerRef: ptr.To("root"), SpecDefinition: &v1alpha1.SpecDefinition{PodSpecPath: ptr.To(".spec")}, PodSelector: &v1alpha1.PodSelector{ComponentTypeSelector: &v1alpha1.ComponentTypeSelector{KeyPath: `.metadata.labels["role"]`, Value: ptr.To("worker")}, ReplicaSelector: &v1alpha1.ReplicaSelector{KeyPath: `.metadata.labels["replica"]`}}}}
	f.resolver, err = New(f.index, Options{Definitions: []*v1alpha1.Karta{f.definition}, Cache: &CacheOptions{MaxWorkloads: 4, MaxPods: 8}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func attributionQuery(t *testing.T, f *attributionFixture) *Workload {
	t.Helper()
	w, err := f.resolver.Workload(t.Context(), reference(f.root))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func attributionApply(t *testing.T, f *attributionFixture, changes Changes) {
	t.Helper()
	var err error
	f.generation, err = f.index.Apply(t.Context(), f.generation, changes)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAttributionCacheOptionsAndDisabled(t *testing.T) {
	f := attributionFixtureFor(t, 1)
	for _, reader := range []Reader{nil, (*Index)(nil), wrappedCacheIndex{f.index}} {
		if _, err := New(reader, Options{Cache: &CacheOptions{MaxWorkloads: 1, MaxPods: 1}}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("reader %T accepted: %v", reader, err)
		}
	}
	for _, limits := range []CacheOptions{{0, 1}, {1, 0}, {-1, 1}, {1, -1}} {
		if _, err := New(f.index, Options{Cache: &limits}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("limits %v accepted: %v", limits, err)
		}
	}
	if _, err := New(nil, Options{}); err != nil {
		t.Fatal(err)
	}
	options := &CacheOptions{MaxWorkloads: 2, MaxPods: 3}
	r, err := New(f.index, Options{Cache: options})
	if err != nil {
		t.Fatal(err)
	}
	options.MaxPods = 0
	if r.cache.maxPods != 3 {
		t.Fatal("constructor retained mutable options")
	}
	disabled, err := New(f.index, Options{Definitions: []*v1alpha1.Karta{f.definition}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := disabled.Workload(t.Context(), reference(f.root))
	if err != nil {
		t.Fatal(err)
	}
	second, err := disabled.Workload(t.Context(), reference(f.root))
	if err != nil {
		t.Fatal(err)
	}
	if disabled.cache != nil || first.prepared == second.prepared {
		t.Fatal("nil option reused attribution")
	}
	snapshot, err := f.index.snapshotWorkload(t.Context(), reference(f.root), true, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.podRevisions != nil {
		t.Fatal("disabled snapshot allocated cache key vector")
	}
	cached := attributionQuery(t, f)
	if !reflect.DeepEqual(first.Object(), cached.Object()) || !reflect.DeepEqual(first.Pods(), cached.Pods()) || !reflect.DeepEqual(first.memberships, cached.memberships) {
		t.Fatal("disabled/cold output differs")
	}
}

func TestAttributionCacheStaticPathEligibility(t *testing.T) {
	for _, expression := range []string{`.`, `.metadata.name`, `.metadata.labels["role"]`, `.spec.containers[0].name`} {
		if !staticAttributionPath(expression) {
			t.Errorf("static path bypassed: %s", expression)
		}
	}
	for _, expression := range []string{`now`, `env`, `$ENV`, `$x`, `.x[.key]`, `.x[now]`, `.x["a\(.y)"]`, `.x[]`, `.x?`, `.x[0:1]`, `.x[-1]`, `(.x)`, `.x | .y`, `.x, .y`, `.x == "a"`, `.x = 1`, `(.x |= now)`, `.x[0 | now]`, `def x: .x; x`, `"x\(now)"`, `error("x")`, `[`, `module {}; .`, `import "x" as x; .`} {
		if staticAttributionPath(expression) {
			t.Errorf("unsupported expression cached: %s", expression)
		}
	}
	f := attributionFixtureFor(t, 0)
	if !cacheableAttribution(f.definition) {
		t.Fatal("fixture not eligible")
	}
	definition := f.definition.DeepCopy()
	definition.Spec.StructureDefinition.RootComponent.InstanceIdPath = ptr.To(".spec.ids")
	if cacheableAttribution(definition) {
		t.Fatal("instance declaration cached")
	}
}

func TestAttributionCacheCopiesAndFreshObservation(t *testing.T) {
	f := attributionFixtureFor(t, 2)
	first := attributionQuery(t, f)
	second := attributionQuery(t, f)
	if first == second || first.prepared != second.prepared || &first.memberships[0] != &second.memberships[0] {
		t.Fatal("hit did not attach shared derived state to fresh workload")
	}
	if first.Observation() == second.Observation() || second.Observation().Started.After(second.Observation().Completed) {
		t.Fatal("observation reused or invalid")
	}
	rootCopy := first.Object()
	if err := unstructured.SetNestedField(rootCopy.Object, "Changed", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(rootCopy.Object, "changed", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	definitionCopy := first.Definition()
	definitionCopy.Spec.StructureDefinition.ChildComponents[0].Name = "changed"
	whole := first.Pods()
	whole[0].Labels["role"] = "changed"
	whole[0].Spec.Containers[0].Args[0] = "changed"
	component, err := first.Component("worker")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := component.Instance("")
	if err != nil {
		t.Fatal(err)
	}
	replica, err := instance.Replica("a")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []interface{ Pods() ([]*corev1.Pod, error) }{component, instance, replica} {
		pods, err := scope.Pods()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pods, f.pods) {
			t.Fatal("scope body or independent copies changed")
		}
		pods[0].Labels["role"] = "changed-scope"
		pods[0].Spec.Containers[0].Args[0] = "changed-scope"
	}
	desired, err := first.DesiredTree(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	desired.Root.Name = "changed-tree"
	freshTree, err := second.DesiredTree(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if freshTree.Root.Name != "root" {
		t.Fatal("desired tree aliases cached fields")
	}
	third := attributionQuery(t, f)
	if !reflect.DeepEqual(third.Object(), f.root) || !reflect.DeepEqual(third.Pods(), f.pods) || third.Definition().Spec.StructureDefinition.ChildComponents[0].Name != "worker" {
		t.Fatal("caller mutation escaped returned copies")
	}
}

func TestAttributionCacheGenericAndZeroPodRoots(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := attributionFixtureFor(t, count)
			known := attributionQuery(t, f)
			knownHit := attributionQuery(t, f)
			if known.prepared != knownHit.prepared {
				t.Fatal("known empty/member root did not hit")
			}
			generic, err := New(f.index, Options{Cache: &CacheOptions{MaxWorkloads: 2, MaxPods: 4}})
			if err != nil {
				t.Fatal(err)
			}
			f.resolver = generic
			first := attributionQuery(t, f)
			second := attributionQuery(t, f)
			if !first.Generic() || !second.Generic() || &first.paths["pods"][0] != &second.paths["pods"][0] {
				t.Fatal("generic attribution not reused")
			}
			if !reflect.DeepEqual(first.Pods(), known.Pods()) {
				t.Fatal("generic output differs")
			}
			if _, err := second.DesiredTree(t.Context()); !errors.Is(err, ErrUnsupported) {
				t.Fatal("generic desired tree contract changed")
			}
			other := indexTestObject("other", "team", indexBundleGVK, nil)
			attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{other}})
			otherResult, err := generic.Workload(t.Context(), reference(other))
			if err != nil {
				t.Fatal(err)
			}
			if otherResult.Root().UID == second.Root().UID || len(otherResult.Pods()) != 0 {
				t.Fatal("zero-Pod root identity collapsed")
			}
		})
	}
}

func TestAttributionCachePublicationIdentity(t *testing.T) {
	f := attributionFixtureFor(t, 2)
	first := attributionQuery(t, f)
	unrelated := indexTestObject("unrelated", "team", indexBundleGVK, nil)
	attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{unrelated}})
	hit := attributionQuery(t, f)
	if first.prepared != hit.prepared || hit.Observation().Generation != f.generation || hit.Observation().Generation == first.Observation().Generation {
		t.Fatal("unrelated generation invalidated attribution or froze observation")
	}
	f.root = f.root.DeepCopy()
	f.root.Object["status"] = map[string]any{"phase": "Done"}
	attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{f.root}})
	changed := attributionQuery(t, f)
	if changed.prepared == hit.prepared || !reflect.DeepEqual(changed.Object(), f.root) {
		t.Fatal("same-RV root edit reused prior revision")
	}
	for _, field := range []string{"spec", "status", "label"} {
		pod := f.pods[0].DeepCopy()
		switch field {
		case "spec":
			pod.Spec.Containers[0].Args[0] = "updated"
		case "status":
			pod.Status.Phase = corev1.PodSucceeded
		case "label":
			pod.Labels["replica"] = "b"
		}
		object, err := podObject(pod)
		if err != nil {
			t.Fatal(err)
		}
		attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{object}})
		f.pods[0] = pod
		next := attributionQuery(t, f)
		if next.prepared == changed.prepared || !reflect.DeepEqual(next.Pods(), f.pods) {
			t.Fatalf("same-RV Pod %s edit reused old state", field)
		}
		changed = next
	}
	attributionApply(t, f, Changes{Deletes: []ObjectRef{podReference(f.pods[1])}})
	removed := attributionQuery(t, f)
	if len(removed.Pods()) != 1 || removed.prepared == changed.prepared {
		t.Fatal("deleted member remained cached")
	}
	added := indexTestPod("pod-added", "team", f.root)
	added.Labels = map[string]string{"role": "worker", "replica": "a"}
	object, err := podObject(added)
	if err != nil {
		t.Fatal(err)
	}
	attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{object}})
	if got := attributionQuery(t, f); len(got.Pods()) != 2 || got.prepared == removed.prepared {
		t.Fatal("new member absent from cache")
	}
	beforeReplace := attributionQuery(t, f)
	f.dataset.Objects = []*unstructured.Unstructured{f.root}
	f.dataset.Pods = f.pods
	f.generation, err = f.index.Replace(t.Context(), f.dataset)
	if err != nil {
		t.Fatal(err)
	}
	if after := attributionQuery(t, f); after.prepared == beforeReplace.prepared {
		t.Fatal("Replace preserved old record identities")
	}
}

func TestAttributionCacheReparentAndInvalidation(t *testing.T) {
	f := attributionFixtureFor(t, 1)
	first := attributionQuery(t, f)
	other := indexTestObject("other", "team", indexBundleGVK, nil)
	pod := f.pods[0].DeepCopy()
	pod.OwnerReferences = []metav1.OwnerReference{indexTestOwner(other)}
	object, err := podObject(pod)
	if err != nil {
		t.Fatal(err)
	}
	attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{object}})
	if _, err := f.resolver.Workload(t.Context(), reference(f.root)); !errors.Is(err, ErrIncompleteOwnership) {
		t.Fatalf("cache bypassed incomplete namespace: %v", err)
	}
	attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{other}})
	if empty := attributionQuery(t, f); len(empty.Pods()) != 0 || empty.prepared == first.prepared {
		t.Fatal("reparented Pod remained in old scope")
	}
	moved, err := f.resolver.WorkloadForPod(t.Context(), podReference(pod))
	if err != nil || moved.Root().UID != other.GetUID() {
		t.Fatalf("reparented terminal root: %v", err)
	}
	f.index.Invalidate(errors.New("coverage lost"))
	if _, err := f.resolver.Workload(t.Context(), reference(f.root)); !errors.Is(err, ErrNotSynced) {
		t.Fatalf("cache bypassed invalidation: %v", err)
	}
	f.generation, err = f.index.Replace(t.Context(), f.dataset)
	if err != nil {
		t.Fatal(err)
	}
	if got := attributionQuery(t, f); got.prepared == first.prepared || len(got.Pods()) != 1 {
		t.Fatal("authoritative recovery reused old attribution")
	}
	oldRef := reference(f.root)
	replacement := f.root.DeepCopy()
	replacement.SetUID("new-root")
	pod = f.pods[0].DeepCopy()
	pod.OwnerReferences = []metav1.OwnerReference{indexTestOwner(replacement)}
	object, err = podObject(pod)
	if err != nil {
		t.Fatal(err)
	}
	attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{replacement, object}})
	if _, err := f.resolver.Workload(t.Context(), oldRef); !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatalf("retired identity accepted: %v", err)
	}
	fresh, err := f.resolver.Workload(t.Context(), reference(replacement))
	if err != nil || fresh.Root().UID != "new-root" {
		t.Fatalf("replacement root: %v", err)
	}
}

func TestAttributionCacheAliasesAndValidation(t *testing.T) {
	f := attributionFixtureFor(t, 1)
	alias := f.root.DeepCopy()
	gvk := indexBundleGVK
	gvk.Version = "v2"
	alias.SetGroupVersionKind(gvk)
	attributionApply(t, f, Changes{Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{gvk: indexTestMapping(gvk, meta.RESTScopeNamespace)}, Upserts: []*unstructured.Unstructured{f.root, alias}})
	known := attributionQuery(t, f)
	generic, err := f.resolver.Workload(t.Context(), reference(alias))
	if err != nil {
		t.Fatal(err)
	}
	if generic.prepared != nil || generic.Object().GroupVersionKind() != gvk || len(f.resolver.cache.entries) != 2 {
		t.Fatal("served alias reused other version plan")
	}
	if again := attributionQuery(t, f); again.prepared != known.prepared {
		t.Fatal("alias evicted known plan")
	}
	ref := podReference(f.pods[0])
	ref.UID = "wrong"
	if _, err := f.resolver.WorkloadForPod(t.Context(), ref); !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatalf("seed UID validation: %v", err)
	}
	if _, err := f.resolver.Workload(t.Context(), podReference(f.pods[0])); !errors.Is(err, ErrNotWorkloadRoot) {
		t.Fatalf("root validation: %v", err)
	}
	ref = reference(f.root)
	ref.Namespace = "outside"
	if _, err := f.resolver.Workload(t.Context(), ref); !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("scope validation: %v", err)
	}
	child := indexTestObject("child", "team", indexWorkerGVK, f.root)
	pod := f.pods[0].DeepCopy()
	pod.OwnerReferences = []metav1.OwnerReference{indexTestOwner(child)}
	obj, err := podObject(pod)
	if err != nil {
		t.Fatal(err)
	}
	attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{child, obj}})
	r, err := New(f.index, Options{MaxDepth: 1, Definitions: []*v1alpha1.Karta{f.definition}, Cache: &CacheOptions{MaxWorkloads: 2, MaxPods: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Workload(t.Context(), reference(f.root)); err == nil {
		t.Fatal("cached query ignored member depth")
	}
}

func TestAttributionCacheBypassErrorsAndCancellation(t *testing.T) {
	for _, expression := range []string{`now`, `env`, `error("deliberate")`, `.metadata.labels.role | .`} {
		t.Run(expression, func(t *testing.T) {
			f := attributionFixtureFor(t, 1)
			definition := f.definition.DeepCopy()
			definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentTypeSelector.KeyPath = expression
			r, err := New(f.index, Options{Definitions: []*v1alpha1.Karta{definition}, Cache: &CacheOptions{MaxWorkloads: 2, MaxPods: 2}})
			if err != nil {
				t.Fatal(err)
			}
			for j := 0; j < 2; j++ {
				if _, err := r.Workload(t.Context(), reference(f.root)); err != nil {
					t.Fatal(err)
				}
			}
			if len(r.cache.entries) != 0 {
				t.Fatal("arbitrary expression cached")
			}
		})
	}
	f := attributionFixtureFor(t, 1)
	// A static path can still be a runtime type error. Failure-bearing results are never admitted.
	definition := f.definition.DeepCopy()
	definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentTypeSelector.KeyPath = `.metadata.name.x`
	r, err := New(f.index, Options{Definitions: []*v1alpha1.Karta{definition}, Cache: &CacheOptions{MaxWorkloads: 2, MaxPods: 2}})
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.Workload(t.Context(), reference(f.root))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.issues) == 0 || len(r.cache.entries) != 0 {
		t.Fatal("selector failure admitted")
	}
	for _, warm := range []bool{false, true} {
		if warm {
			attributionQuery(t, f)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := f.resolver.Workload(ctx, reference(f.root)); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled query warm=%v: %v", warm, err)
		}
	}
	empty := attributionFixtureFor(t, 0)
	definition = empty.definition.DeepCopy()
	definition.Spec.StructureDefinition.ChildComponents[0].InstanceIdPath = ptr.To(`error("prepare")`)
	definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentInstanceSelector = &v1alpha1.ComponentInstanceSelector{IdPath: `.metadata.labels.instance`}
	plan, err := attribution.Compile(definition)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := empty.index.snapshotWorkload(t.Context(), reference(empty.root), true, 32, true)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the admission guard even if a future eligibility policy broadens.
	w, err = empty.resolver.cachedWorkload(t.Context(), snapshot, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.issues) != 0 || len(empty.resolver.cache.entries) != 0 {
		t.Fatal("zero-Pod preparation failure admitted")
	}
}

func TestAttributionCacheBoundsAndConcurrentFills(t *testing.T) {
	cache := newAttributionCache(CacheOptions{MaxWorkloads: 2, MaxPods: 3})
	a := attributionKey{root: &indexRevision{}}
	b := attributionKey{root: &indexRevision{}}
	c := attributionKey{root: &indexRevision{}}
	rows := []*indexRevision{{}, {}}
	first, second := &derivedAttribution{}, &derivedAttribution{}
	for _, key := range []attributionKey{a, b} {
		if err := cache.put(t.Context(), key, rows, 2, first); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 1 || cache.pods != 2 || cache.entries[a] != nil {
		t.Fatal("aggregate row bound not enforced")
	}
	if err := cache.put(t.Context(), c, nil, 2, first); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.get(t.Context(), b, rows); err != nil {
		t.Fatal(err)
	}
	if err := cache.put(t.Context(), a, nil, 2, first); err != nil {
		t.Fatal(err)
	}
	if cache.entries[c] != nil || len(cache.entries) != 2 {
		t.Fatal("LRU entry bound not enforced for empty workload")
	}
	if err := cache.put(t.Context(), b, rows, 1, second); err != nil {
		t.Fatal(err)
	}
	got, err := cache.get(t.Context(), b, rows)
	if err != nil || got != first {
		t.Fatal("older concurrent fill replaced newer revision")
	}
	rows[0] = &indexRevision{}
	if got, err := cache.get(t.Context(), b, rows); err != nil || got != nil {
		t.Fatal("retained key vector aliases caller or ignores mismatch")
	}
	f := attributionFixtureFor(t, 2)
	f.resolver.cache = newAttributionCache(CacheOptions{MaxWorkloads: 1, MaxPods: 1})
	attributionQuery(t, f)
	if len(f.resolver.cache.entries) != 0 {
		t.Fatal("oversized workload cached")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cache.put(ctx, c, nil, 3, second); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled fill admitted")
	}
}

func TestAttributionCacheConcurrentQueriesAndPublication(t *testing.T) {
	f := attributionFixtureFor(t, 2)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				w, err := f.resolver.Workload(t.Context(), reference(f.root))
				if errors.Is(err, ErrNotSynced) {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				if len(w.Pods()) != 2 {
					t.Error("partial snapshot")
				}
				component, err := w.Component("worker")
				if err != nil {
					t.Error(err)
					return
				}
				pods, err := component.Pods()
				if err != nil {
					t.Error(err)
					return
				}
				pods[0].Labels["role"] = "caller mutation"
			}
		}()
	}
	for j := 0; j < 20; j++ {
		if j%5 == 0 {
			f.index.Invalidate(errors.New("test rebuild"))
			var err error
			f.generation, err = f.index.Replace(t.Context(), f.dataset)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			root := f.root.DeepCopy()
			root.Object["status"] = map[string]any{"phase": fmt.Sprint(j)}
			attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{root}})
		}
	}
	wg.Wait()
	if len(f.resolver.cache.entries) > f.resolver.cache.maxWorkloads || f.resolver.cache.pods > f.resolver.cache.maxPods {
		t.Fatal("concurrent fills exceeded bounds")
	}
}

func TestAttributionCacheDoesNotRetainPublishedBodies(t *testing.T) {
	f := attributionFixtureFor(t, 1)
	oldRoot, oldPod, oldResult := attributionWeakBodies(t, f)
	if len(f.resolver.cache.entries) != 1 {
		t.Fatal("cache not populated")
	}
	var err error
	f.generation, err = f.index.Replace(t.Context(), f.dataset)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.GC()
	if oldRoot.Value() != nil || oldPod.Value() != nil || oldResult.Value() != nil {
		t.Fatal("derived cache retained retired full object or Pod bodies")
	}
	runtime.KeepAlive(f)
}

func attributionWeakBodies(t *testing.T, f *attributionFixture) (weak.Pointer[unstructured.Unstructured], weak.Pointer[corev1.Pod], weak.Pointer[unstructured.Unstructured]) {
	t.Helper()
	w := attributionQuery(t, f)
	return weak.Make(f.index.state.records[f.root.GetUID()].object), weak.Make(f.index.state.records[f.pods[0].UID].pod), weak.Make(w.object)
}
