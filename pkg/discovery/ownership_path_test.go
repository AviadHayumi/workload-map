// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

func TestPodReference(t *testing.T) {
	pod := sdkPod("member", nil, nil)
	pod.TypeMeta = metav1.TypeMeta{}
	want := sdkPodRef(pod)
	got := discovery.PodReference(pod)
	if got != want {
		t.Fatalf("PodReference without TypeMeta = %+v, want %+v", got, want)
	}
	pod.Name = "changed"
	if got != want {
		t.Fatal("reference changed with source Pod")
	}
	if got := discovery.PodReference(nil); got != (discovery.ObjectRef{}) {
		t.Fatalf("PodReference(nil) = %+v", got)
	}
}

func TestOwnershipPathReadsOnlyRequestedChain(t *testing.T) {
	ctx := t.Context()
	root := sdkObject(sdkDeploymentGVK, "team", "root", "root", nil)
	// Invalid desired state must not affect a physical ownership query.
	root.Object["spec"] = "invalid desired state"
	middle := sdkObject(sdkReplicaSetGVK, "team", "middle", "middle", root)
	pod := sdkPod("member", middle, nil)
	pod.TypeMeta = metav1.TypeMeta{}
	missing := sdkObject(sdkReplicaSetGVK, "team", "missing", "missing", nil)
	unrelated := sdkPod("unrelated", missing, nil)
	reader := sdkNewReader(t, []*unstructured.Unstructured{root, middle}, pod, unrelated)
	reader.listError = errors.New("Pod collection must not run")
	captured := &sdkCapturedReader{sdkReader: reader, captureError: errors.New("workload capture must not run")}
	resolver := sdkResolver(t, captured)
	want := []discovery.ObjectRef{sdkPodRef(pod), sdkRef(middle), sdkRef(root)}
	path, err := resolver.OwnershipPath(ctx, discovery.PodReference(pod))
	if err != nil || !reflect.DeepEqual(path, want) {
		t.Fatalf("OwnershipPath = %+v, %v; want %+v", path, err, want)
	}
	var reads []string
	for _, call := range reader.calls {
		if strings.HasPrefix(call, "get:") {
			reads = append(reads, call)
		}
		if strings.HasPrefix(call, "list:") {
			t.Fatalf("ownership path collected Pods: %v", reader.calls)
		}
	}
	if !reflect.DeepEqual(reads, []string{"get:uid-member", "get:middle", "get:root"}) || captured.captures != 0 {
		t.Fatalf("reads=%v captures=%d", reads, captured.captures)
	}
	path[0].Name = "changed"
	path, err = resolver.OwnershipPath(ctx, discovery.PodReference(pod))
	if err != nil || !reflect.DeepEqual(path, want) {
		t.Fatalf("caller mutation changed a later path: %+v, %v", path, err)
	}
	path, err = resolver.OwnershipPath(ctx, sdkRef(root))
	if err != nil || !reflect.DeepEqual(path, []discovery.ObjectRef{sdkRef(root)}) {
		t.Fatalf("terminal object path = %+v, %v", path, err)
	}
}

func TestOwnershipPathDepthAndFailures(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		for _, test := range []struct {
			name     string
			maxDepth int
			change   func(*unstructured.Unstructured, *unstructured.Unstructured) []*unstructured.Unstructured
			want     error
			prefix   int
		}{
			{name: "default depth"},
			{name: "exact depth", maxDepth: 2},
			{name: "depth exceeded", maxDepth: 1, want: ownership.ErrDepthExceeded, prefix: 2},
			{name: "missing owner", change: func(root, middle *unstructured.Unstructured) []*unstructured.Unstructured {
				owner := sdkOwner(root)
				owner.Name = "absent"
				middle.SetOwnerReferences([]metav1.OwnerReference{owner})
				return []*unstructured.Unstructured{middle}
			}, want: ownership.ErrMissing, prefix: 2},
			{name: "recreated owner", change: func(root, middle *unstructured.Unstructured) []*unstructured.Unstructured {
				owner := sdkOwner(root)
				owner.UID = "old-root"
				middle.SetOwnerReferences([]metav1.OwnerReference{owner})
				return []*unstructured.Unstructured{middle}
			}, want: ownership.ErrUIDMismatch, prefix: 2},
			{name: "cycle", change: func(root, middle *unstructured.Unstructured) []*unstructured.Unstructured {
				root.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(middle)})
				return []*unstructured.Unstructured{root}
			}, want: ownership.ErrCycle, prefix: 3},
			{name: "multiple controllers", change: func(root, middle *unstructured.Unstructured) []*unstructured.Unstructured {
				root.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(middle), sdkOwner(middle)})
				return []*unstructured.Unstructured{root}
			}, want: ownership.ErrMultipleControllers, prefix: 3},
		} {
			t.Run(fmt.Sprintf("indexed=%t/%s", indexed, test.name), func(t *testing.T) {
				root := sdkObject(sdkDeploymentGVK, "team", "root", "root", nil)
				middle := sdkObject(sdkReplicaSetGVK, "team", "middle", "middle", root)
				pod := sdkPod("member", middle, nil)
				reader := sdkNewReader(t, []*unstructured.Unstructured{root, middle}, pod)
				var source discovery.Reader = reader
				if indexed {
					index, generation := sdkSnapshotIndex(t, reader, "team")
					source = index
					if test.change != nil {
						if _, err := index.Apply(t.Context(), generation, discovery.Changes{Upserts: test.change(root, middle)}); err != nil {
							t.Fatal(err)
						}
					}
				} else if test.change != nil {
					test.change(root, middle)
				}
				resolver, err := discovery.New(source, discovery.Options{MaxDepth: test.maxDepth})
				if err != nil {
					t.Fatal(err)
				}
				path, err := resolver.OwnershipPath(t.Context(), sdkPodRef(pod))
				if test.want == nil {
					if err != nil || len(path) != 3 {
						t.Fatalf("path=%+v err=%v", path, err)
					}
					return
				}
				var failure *ownership.Error
				if path != nil || !errors.Is(err, test.want) || !errors.As(err, &failure) || len(failure.Path) != test.prefix {
					t.Fatalf("path=%+v error=%v failure=%+v", path, err, failure)
				}
			})
		}
	}
}

func TestIndexOwnershipPathScopeAliasesAndUnrelatedFailure(t *testing.T) {
	ctx := t.Context()
	root := sdkObject(sdkBundleGVK, "", "root", "root", nil)
	alias := root.DeepCopy()
	aliasGVK := sdkBundleGVK
	aliasGVK.Version = "v2"
	alias.SetGroupVersionKind(aliasGVK)
	middle := sdkObject(sdkReplicaSetGVK, "team", "middle", "middle", alias)
	pod := sdkPod("member", middle, nil)
	reader := sdkNewReader(t, []*unstructured.Unstructured{root, alias, middle}, pod)
	index := discovery.NewIndex()
	generation, err := index.Replace(ctx, discovery.Dataset{Namespace: "team", Objects: []*unstructured.Unstructured{root, alias, middle}, Pods: []*corev1.Pod{pod}, Mappings: reader.mappings})
	if err != nil {
		t.Fatal(err)
	}
	resolver := sdkResolver(t, index)
	want := []discovery.ObjectRef{sdkPodRef(pod), sdkRef(middle), sdkRef(alias)}
	path, err := resolver.OwnershipPath(ctx, sdkPodRef(pod))
	if err != nil || !reflect.DeepEqual(path, want) {
		t.Fatalf("namespaced index to cluster alias = %+v, %v", path, err)
	}
	missing := sdkObject(sdkReplicaSetGVK, "team", "missing", "missing", nil)
	unrelated := sdkObject(sdkReplicaSetGVK, "team", "unrelated", "unrelated", missing)
	if _, err := index.Apply(ctx, generation, discovery.Changes{Upserts: []*unstructured.Unstructured{unrelated}}); err != nil {
		t.Fatal(err)
	}
	path[1].Name = "changed"
	path, err = resolver.OwnershipPath(ctx, sdkPodRef(pod))
	if err != nil || !reflect.DeepEqual(path, want) {
		t.Fatalf("unrelated failure or caller mutation changed path = %+v, %v", path, err)
	}
	for _, test := range []struct {
		ref  discovery.ObjectRef
		want error
	}{
		{discovery.ObjectRef{}, discovery.ErrInvalidInput},
		{discovery.ObjectRef{GVK: sdkPodGVK, Namespace: "other", Name: pod.Name, UID: pod.UID}, discovery.ErrInsufficientScope},
		{discovery.ObjectRef{GVK: sdkPodGVK, Namespace: "team", Name: pod.Name, UID: "old"}, ownership.ErrUIDMismatch},
		{discovery.ObjectRef{GVK: aliasGVK, Namespace: "team", Name: root.GetName(), UID: root.GetUID()}, ownership.ErrScope},
	} {
		if path, err := resolver.OwnershipPath(ctx, test.ref); path != nil || !errors.Is(err, test.want) {
			t.Fatalf("OwnershipPath(%+v) = %+v, %v; want %v", test.ref, path, err, test.want)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if path, err := resolver.OwnershipPath(canceled, sdkPodRef(pod)); path != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled path=%+v error=%v", path, err)
	}
	index.Invalidate(errors.New("watch lost"))
	if path, err := resolver.OwnershipPath(ctx, sdkPodRef(pod)); path != nil || !errors.Is(err, discovery.ErrNotSynced) {
		t.Fatalf("invalidated path=%+v error=%v", path, err)
	}
}

func TestIndexOwnershipPathReparentingUsesOneGeneration(t *testing.T) {
	ctx := t.Context()
	root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
	other := sdkObject(sdkBundleGVK, "team", "other", "other", nil)
	first := sdkObject(sdkReplicaSetGVK, "team", "first", "first", root)
	second := sdkObject(sdkReplicaSetGVK, "team", "second", "second", other)
	pod := sdkPod("member", first, nil)
	index, generation := sdkSnapshotIndex(t, sdkNewReader(t, []*unstructured.Unstructured{root, other, first, second}, pod), "team", sdkRef(second))
	resolver := sdkResolver(t, index)
	ref := sdkPodRef(pod)
	before, err := resolver.OwnershipPath(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for n := 0; n < 101; n++ {
			active, idle := first, second
			if n%2 == 0 {
				active, idle = second, first
			}
			active.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(root)})
			idle.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(other)})
			pod.OwnerReferences = []metav1.OwnerReference{sdkOwner(active)}
			version := fmt.Sprint(n + 2)
			first.SetResourceVersion(version)
			second.SetResourceVersion(version)
			pod.ResourceVersion = version
			data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
			if err != nil {
				t.Error(err)
				return
			}
			generation, err = index.Apply(ctx, generation, discovery.Changes{Upserts: []*unstructured.Unstructured{first, second, {Object: data}}})
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for n := 0; n < 300; n++ {
		path, err := resolver.OwnershipPath(ctx, ref)
		if err != nil || len(path) != 3 || path[2] != sdkRef(root) {
			t.Errorf("path mixed reparenting generations: %+v, %v", path, err)
			break
		}
	}
	workers.Wait()
	after, err := resolver.OwnershipPath(ctx, ref)
	if err != nil || len(after) != 3 || after[1] != sdkRef(second) || before[1] != sdkRef(first) || before[2] != sdkRef(root) {
		t.Fatalf("reparenting changed prior path or missed final state: before=%+v after=%+v err=%v", before, after, err)
	}
}
