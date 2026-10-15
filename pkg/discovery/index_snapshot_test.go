// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

// The wrapper deliberately exercises the public, defensive capture boundary.
type sdkDefensiveIndex struct{ *discovery.Index }

func sdkSnapshotIndex(t *testing.T, reader *sdkReader, namespace string, roots ...discovery.ObjectRef) (*discovery.Index, uint64) {
	t.Helper()
	dataset, err := discovery.Collect(context.Background(), reader, namespace, roots...)
	if err != nil {
		t.Fatal(err)
	}
	index := discovery.NewIndex()
	generation, err := index.Replace(context.Background(), dataset)
	if err != nil {
		t.Fatal(err)
	}
	return index, generation
}

func sdkSnapshotParity(t *testing.T, ctx context.Context, index *discovery.Index, seed discovery.ObjectRef, pod bool, options discovery.Options) (*discovery.Workload, error) {
	t.Helper()
	var results [2]*discovery.Workload
	var failures [2]error
	for n, reader := range []discovery.Reader{index, sdkDefensiveIndex{index}} {
		resolver, err := discovery.New(reader, options)
		if err != nil {
			t.Fatal(err)
		}
		if pod {
			results[n], failures[n] = resolver.WorkloadForPod(ctx, seed)
		} else {
			results[n], failures[n] = resolver.Workload(ctx, seed)
		}
	}
	if (failures[0] == nil) != (failures[1] == nil) {
		t.Fatalf("fast/capture errors: %v / %v", failures[0], failures[1])
	}
	if failures[0] != nil {
		if results[0] != nil || results[1] != nil || failures[0].Error() != failures[1].Error() {
			t.Fatalf("fast/capture failure differs: %v / %v", failures[0], failures[1])
		}
		for _, kind := range []error{context.Canceled, discovery.ErrNotSynced, discovery.ErrIncompleteOwnership, discovery.ErrInsufficientScope, discovery.ErrNotWorkloadRoot, ownership.ErrScope, ownership.ErrUIDMismatch, ownership.ErrMissing, ownership.ErrCycle, ownership.ErrDepthExceeded} {
			if errors.Is(failures[0], kind) != errors.Is(failures[1], kind) {
				t.Fatalf("error classification differs for %v", kind)
			}
		}
		var fast, captured *ownership.Error
		if errors.As(failures[0], &fast) != errors.As(failures[1], &captured) || (fast != nil && (fast.Expected != captured.Expected || fast.Actual != captured.Actual || !reflect.DeepEqual(fast.Path, captured.Path))) {
			t.Fatalf("typed ownership diagnostics differ: %#v / %#v", fast, captured)
		}
		return nil, failures[0]
	}
	fast, captured := results[0], results[1]
	if fast.Root() != captured.Root() || !reflect.DeepEqual(fast.Object(), captured.Object()) || !reflect.DeepEqual(fast.Pods(), captured.Pods()) || !reflect.DeepEqual(fast.Definition(), captured.Definition()) || !reflect.DeepEqual(fast.MembershipIssues(), captured.MembershipIssues()) {
		t.Fatal("fast/capture workload contents differ")
	}
	a, b := fast.Observation(), captured.Observation()
	if a.Source != b.Source || a.Namespace != b.Namespace || a.Generation != b.Generation || a.PodResourceVersion != b.PodResourceVersion || a.Started.After(a.Completed) {
		t.Fatalf("fast/capture observations differ: %+v / %+v", a, b)
	}
	return fast, nil
}

func sdkSnapshotDefinition() *v1alpha1.Karta {
	return &v1alpha1.Karta{Spec: v1alpha1.KartaSpec{StructureDefinition: v1alpha1.StructureDefinition{RootComponent: v1alpha1.ComponentDefinition{
		Name: "bundle", Kind: &v1alpha1.GroupVersionKind{Group: sdkBundleGVK.Group, Version: sdkBundleGVK.Version, Kind: sdkBundleGVK.Kind},
		StatusDefinition: &v1alpha1.StatusDefinition{}, SpecDefinition: &v1alpha1.SpecDefinition{PodSpecPath: ptr.To(".spec.template.spec")},
	}}}}
}

func TestIndexSnapshotAliasAndEmptyParity(t *testing.T) {
	ctx := context.Background()
	root := sdkObject(sdkBundleGVK, "team", "bundle", "bundle", nil)
	alias := root.DeepCopy()
	gvk := sdkBundleGVK
	gvk.Version = "v2"
	alias.SetGroupVersionKind(gvk)
	pod := sdkPod("member", alias, nil)
	empty := sdkObject(gvk, "team", "empty", "empty", nil)
	standalone := sdkPod("standalone", nil, nil)
	reader := sdkNewReader(t, []*unstructured.Unstructured{root, alias, empty}, pod, standalone)
	index, generation := sdkSnapshotIndex(t, reader, "team", sdkRef(root), sdkRef(alias), sdkRef(empty))
	options := discovery.Options{Definitions: []*v1alpha1.Karta{sdkSnapshotDefinition()}}
	for _, ref := range []discovery.ObjectRef{sdkRef(root), sdkRef(alias), sdkRef(empty), sdkPodRef(standalone), sdkPodRef(pod)} {
		workload, err := sdkSnapshotParity(t, ctx, index, ref, ref.GVK == sdkPodGVK, options)
		if err != nil {
			t.Fatal(err)
		}
		if ref == sdkRef(empty) && (!workload.Generic() || len(workload.Pods()) != 0) {
			t.Fatal("generic empty workload changed")
		}
		if ref == sdkRef(alias) || ref == sdkPodRef(pod) {
			if !workload.Generic() || workload.Root().GVK != gvk {
				t.Fatal("terminal served version was replaced by the canonical alias")
			}
		}
	}
	alias.SetResourceVersion("2")
	if _, err := index.Apply(ctx, generation, discovery.Changes{Upserts: []*unstructured.Unstructured{alias}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sdkSnapshotParity(t, ctx, index, sdkRef(root), false, options); err == nil {
		t.Fatal("dropped alias remained available")
	}
	if _, err := sdkSnapshotParity(t, ctx, index, sdkPodRef(pod), true, options); err != nil {
		t.Fatal(err)
	}
}

func TestIndexSnapshotDepthAndOwnershipFailureParity(t *testing.T) {
	ctx := context.Background()
	root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
	first := sdkObject(sdkReplicaSetGVK, "team", "first", "first", root)
	second := sdkObject(sdkReplicaSetGVK, "team", "second", "second", first)
	shallow, deep := sdkPod("shallow", root, nil), sdkPod("deep", second, nil)
	index, generation := sdkSnapshotIndex(t, sdkNewReader(t, []*unstructured.Unstructured{root, first, second}, shallow, deep), "team")
	for _, seed := range []discovery.ObjectRef{sdkRef(root), sdkPodRef(shallow), sdkPodRef(deep)} {
		_, err := sdkSnapshotParity(t, ctx, index, seed, seed.GVK == sdkPodGVK, discovery.Options{MaxDepth: 2})
		if !errors.Is(err, ownership.ErrDepthExceeded) {
			t.Fatalf("deep member was accepted: %v", err)
		}
		if _, err := sdkSnapshotParity(t, ctx, index, seed, seed.GVK == sdkPodGVK, discovery.Options{MaxDepth: 3}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sdkSnapshotParity(t, ctx, index, sdkRef(first), false, discovery.Options{}); !errors.Is(err, discovery.ErrNotWorkloadRoot) {
		t.Fatal(err)
	}
	second.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(root)})
	second.SetResourceVersion("2")
	var err error
	generation, err = index.Apply(ctx, generation, discovery.Changes{Upserts: []*unstructured.Unstructured{second}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdkSnapshotParity(t, ctx, index, sdkRef(root), false, discovery.Options{MaxDepth: 2}); err != nil {
		t.Fatalf("reparenting did not refresh cached depth: %v", err)
	}
	generation, err = index.Apply(ctx, generation, discovery.Changes{Deletes: []discovery.ObjectRef{sdkRef(second)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdkSnapshotParity(t, ctx, index, sdkRef(root), false, discovery.Options{MaxDepth: 1}); !errors.Is(err, ownership.ErrMissing) {
		t.Fatalf("published missing owner must precede caller depth: %v", err)
	}
	root.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(first)})
	if _, err := index.Apply(ctx, generation, discovery.Changes{Upserts: []*unstructured.Unstructured{root}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sdkSnapshotParity(t, ctx, index, sdkRef(root), false, discovery.Options{MaxDepth: 1}); !errors.Is(err, ownership.ErrCycle) {
		t.Fatal(err)
	}
}

func TestIndexSnapshotScopeAndInvalidationParity(t *testing.T) {
	ctx := context.Background()
	root := sdkObject(sdkBundleGVK, "", "cluster", "cluster", nil)
	pod := sdkPod("one", root, nil)
	reader := sdkNewReader(t, []*unstructured.Unstructured{root}, pod)
	index, _ := sdkSnapshotIndex(t, reader, "team")
	if _, err := sdkSnapshotParity(t, ctx, index, sdkPodRef(pod), true, discovery.Options{}); !errors.Is(err, discovery.ErrInsufficientScope) {
		t.Fatal(err)
	}
	wrongScope := sdkRef(root)
	wrongScope.Namespace = "team"
	if _, err := sdkSnapshotParity(t, ctx, index, wrongScope, false, discovery.Options{}); !errors.Is(err, discovery.ErrInsufficientScope) {
		t.Fatalf("coverage failure must precede invalid seed namespace: %v", err)
	}
	index, _ = sdkSnapshotIndex(t, reader, "")
	if _, err := sdkSnapshotParity(t, ctx, index, sdkPodRef(pod), true, discovery.Options{}); err != nil {
		t.Fatal(err)
	}
	wrong := sdkRef(root)
	wrong.Namespace = "team"
	if _, err := sdkSnapshotParity(t, ctx, index, wrong, false, discovery.Options{}); !errors.Is(err, ownership.ErrScope) {
		t.Fatal(err)
	}
	wrong = sdkRef(root)
	wrong.UID = "old-lifetime"
	if _, err := sdkSnapshotParity(t, ctx, index, wrong, false, discovery.Options{}); !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := sdkSnapshotParity(t, canceled, index, sdkRef(root), false, discovery.Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	index.Invalidate(errors.New("feed interrupted"))
	if _, err := sdkSnapshotParity(t, ctx, index, sdkRef(root), false, discovery.Options{}); !errors.Is(err, discovery.ErrNotSynced) {
		t.Fatal(err)
	}
}

func TestIndexSnapshotSharedPodsRemainImmutable(t *testing.T) {
	ctx := context.Background()
	root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
	root.Object["spec"].(map[string]any)["instances"] = []any{"original"}
	pod := sdkPod("member", root, map[string]string{"instance": "original"})
	index, generation := sdkSnapshotIndex(t, sdkNewReader(t, []*unstructured.Unstructured{root}, pod), "team")
	definition := sdkSnapshotDefinition()
	component := &definition.Spec.StructureDefinition.RootComponent
	component.InstanceIdPath = ptr.To(`.spec.instances | map("mutated") | .[]`)
	component.PodSelector = &v1alpha1.PodSelector{ComponentInstanceSelector: &v1alpha1.ComponentInstanceSelector{IdPath: `(.metadata.labels + {"instance": "mutated"}).instance`}}
	options := discovery.Options{Definitions: []*v1alpha1.Karta{definition}}
	workload, err := sdkSnapshotParity(t, ctx, index, sdkRef(root), false, options)
	if err != nil {
		t.Fatal(err)
	}
	view := sdkInstance(t, sdkChild(t, workload, "bundle"), "mutated")
	sdkAssertScope(t, view, string(pod.UID))
	workload.Object().SetName("caller mutation")
	workload.Pods()[0].Labels["instance"] = "caller mutation"
	workload.Definition().Spec.StructureDefinition.RootComponent.Name = "caller mutation"
	members, err := view.Pods()
	if err != nil {
		t.Fatal(err)
	}
	members[0].Labels["instance"] = "caller mutation"
	stored, err := index.Get(ctx, sdkRef(root))
	if err != nil || !reflect.DeepEqual(stored, root) {
		t.Fatalf("jq or getters mutated indexed root: %v", err)
	}
	listed, err := index.ListPods(ctx, "team")
	if err != nil || listed.Items[0].Labels["instance"] != "original" {
		t.Fatalf("jq or getters mutated indexed Pod: %v", err)
	}
	pod.Labels["instance"] = "updated"
	pod.ResourceVersion = "2"
	root.SetResourceVersion("2")
	root.Object["spec"].(map[string]any)["instances"] = []any{"updated"}
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Apply(ctx, generation, discovery.Changes{Upserts: []*unstructured.Unstructured{root, {Object: data}}}); err != nil {
		t.Fatal(err)
	}
	fresh, err := sdkSnapshotParity(t, ctx, index, sdkRef(root), false, options)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Pods()[0].Labels["instance"] != "updated" || workload.Pods()[0].Labels["instance"] != "original" || fresh.Object().GetResourceVersion() != "2" || workload.Object().GetResourceVersion() != "1" {
		t.Fatal("publication changed a retained observation")
	}
	index.Invalidate(nil)
	dataset, err := discovery.Collect(ctx, sdkNewReader(t, []*unstructured.Unstructured{root}, pod), "team")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	sdkAssertScope(t, view, string(pod.UID))
	if workload.Pods()[0].Labels["instance"] != "original" || workload.Object().GetResourceVersion() != "1" {
		t.Fatal("replacement changed a retained observation")
	}
}

func TestIndexSnapshotConcurrentPublication(t *testing.T) {
	ctx := context.Background()
	root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
	pod := sdkPod("member", root, nil)
	index, generation := sdkSnapshotIndex(t, sdkNewReader(t, []*unstructured.Unstructured{root}, pod), "team")
	resolver := sdkResolver(t, index)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for n := 2; n < 42; n++ {
			version := fmt.Sprint(n)
			root.SetResourceVersion(version)
			pod.ResourceVersion = version
			data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
			if err != nil {
				t.Error(err)
				return
			}
			generation, err = index.Apply(ctx, generation, discovery.Changes{Upserts: []*unstructured.Unstructured{root, {Object: data}}})
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	seed := discovery.ObjectRef{GVK: sdkBundleGVK, Namespace: "team", Name: "root", UID: "root"}
	for n := 0; n < 60; n++ {
		workload, err := resolver.Workload(ctx, seed)
		if err != nil {
			t.Error(err)
			break
		}
		if workload.Object().GetResourceVersion() != workload.Pods()[0].ResourceVersion {
			t.Error("workload combines publication generations")
			break
		}
	}
	wait.Wait()
}
