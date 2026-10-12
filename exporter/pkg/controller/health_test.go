// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/restmapper"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
)

type syncedInformer struct{ cache.SharedIndexInformer }

func (s syncedInformer) HasSynced() bool { return true }

func fixture(t *testing.T) (*Controller, *dynamicfake.FakeDynamicClient, *unstructured.Unstructured) {
	t.Helper()
	job := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "example", "namespace": "test", "uid": "job-a", "resourceVersion": "1"}, "spec": map[string]any{"parallelism": int64(1), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "worker", "image": "example"}}}}}, "status": map[string]any{"active": int64(1), "conditions": []any{}}}}
	scheme := runtime.NewScheme()
	kinds := map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList", kartaGVR: "KartaList", {Group: "batch", Version: "v1", Resource: "jobs"}: "JobList"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, kinds, job)
	k := kubefake.NewClientset()
	discovery := k.Discovery().(*discoveryfake.FakeDiscovery)
	discovery.Resources = []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}}}, {GroupVersion: "batch/v1", APIResources: []metav1.APIResource{{Name: "jobs", Kind: "Job", Namespaced: true}}}, {GroupVersion: "run.ai/v1alpha1", APIResources: []metav1.APIResource{{Name: "kartas", Kind: "Karta"}}}}
	reg := registry.New()
	definition, err := catalog.Get(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"})
	if err != nil {
		t.Fatal(err)
	}
	reg.Set(definition)
	c, err := New(client, metadatafake.NewSimpleMetadataClient(scheme), k, restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discovery)), reg, owner.New(), store.New(), noop.NewMeterProvider().Meter("test"), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Namespace: "test", HealthInterval: time.Second, HealthMaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	informer := func(items ...any) cache.SharedIndexInformer {
		i := cache.NewSharedIndexInformer(&cache.ListWatch{}, &unstructured.Unstructured{}, 0, cache.Indexers{})
		for _, item := range items {
			if err := i.GetStore().Add(item); err != nil {
				t.Fatal(err)
			}
		}
		return syncedInformer{i}
	}
	c.podInformer = informer()
	c.kartaInformer = informer()
	gvk := job.GroupVersionKind()
	c.watchers[gvk] = &watcher{gvk: gvk, isRoot: true, informer: informer(job.DeepCopy())}
	c.processWorkload(job, false)
	return c, client, job
}

func workloadHealth(t *testing.T, c *Controller) float64 {
	t.Helper()
	for _, h := range c.store.Snapshot().Health {
		if h.Scope == "workload" && h.UID == "job-a" {
			return h.Value(time.Now())
		}
	}
	t.Fatal("missing workload health")
	return -1
}

func TestHealthPermissionFailureAndQuietRecovery(t *testing.T) {
	c, client, _ := fixture(t)
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 1 {
		t.Fatal("valid quiet workload should be healthy")
	}
	denied := true
	client.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		if denied {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "", fmt.Errorf("denied"))
		}
		return false, nil, nil
	})
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 0 {
		t.Fatal("permission loss must invalidate workload")
	}
	denied = false
	c.markEvent()
	if workloadHealth(t, c) != 0 {
		t.Fatal("an unrelated event must not recover health")
	}
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 1 {
		t.Fatal("fresh quiet verification must recover health")
	}
}

func TestHealthCacheLagAndUnknownKind(t *testing.T) {
	c, client, job := fixture(t)
	changed := job.DeepCopy()
	changed.SetResourceVersion("2")
	if _, err := client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace("test").Update(context.Background(), changed, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 0 {
		t.Fatal("successful API access cannot hide a stale cache")
	}
	gvk := schema.GroupVersionKind{Group: "missing.example", Version: "v1", Kind: "Missing"}
	c.options.RequiredKinds = []schema.GroupVersionKind{gvk}
	c.verifyHealth(context.Background())
	found := false
	for _, h := range c.store.Snapshot().Health {
		if h.Scope == "kind" && h.Ref.Kind == "Missing" {
			found = true
			if h.Healthy {
				t.Fatal("unknown kind healthy")
			}
		}
	}
	if !found {
		t.Fatal("unknown required kind must emit zero before workloads exist")
	}
}

func TestCacheMatchesRejectsReplacementUID(t *testing.T) {
	c, _, job := fixture(t)
	replaced := job.DeepCopy()
	replaced.SetUID("replacement")
	if cacheMatches(c.rootWatcherStore(job.GroupVersionKind().GroupKind()), []unstructured.Unstructured{*replaced}) {
		t.Fatal("same name and version must not hide replacement UID")
	}
}

func TestSameWorkloadComparesSets(t *testing.T) {
	a := store.WorkloadRecord{Components: []store.ComponentState{{Component: "worker"}, {Component: "leader"}}}
	b := store.WorkloadRecord{Components: []store.ComponentState{{Component: "leader"}, {Component: "worker"}}}
	if !sameWorkload(a, b) {
		t.Fatal("selector iteration order must not affect health")
	}
	b.Components[1].Instance = "different"
	if sameWorkload(a, b) {
		t.Fatal("identity changes must still invalidate health")
	}
}

func TestMalformedWorkloadDoesNotPoisonPeer(t *testing.T) {
	c, client, job := fixture(t)
	broken := job.DeepCopy()
	broken.SetName("broken")
	broken.SetUID("job-b")
	if err := unstructured.SetNestedField(broken.Object, map[string]any{"invalid": true}, "spec", "parallelism"); err != nil {
		t.Fatal(err)
	}
	jobs := client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace("test")
	if _, err := jobs.Create(context.Background(), broken, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cached := c.rootWatcherStore(job.GroupVersionKind().GroupKind())
	if err := cached.Add(broken); err != nil {
		t.Fatal(err)
	}
	c.processWorkload(broken, false)
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 1 {
		t.Fatal("malformed peer poisoned a valid workload")
	}
	for _, h := range c.store.Snapshot().Health {
		if h.UID == "job-b" && h.Healthy {
			t.Fatal("malformed workload must be unhealthy")
		}
	}
	fixed := broken.DeepCopy()
	if err := unstructured.SetNestedField(fixed.Object, int64(1), "spec", "parallelism"); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Update(context.Background(), fixed, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := cached.Update(fixed); err != nil {
		t.Fatal(err)
	}
	c.processWorkload(fixed, false)
	c.verifyHealth(context.Background())
	for _, h := range c.store.Snapshot().Health {
		if h.UID == "job-b" && !h.Healthy {
			t.Fatal("fresh corrected workload must recover")
		}
	}
}

func TestHealthDetectsMissingAttributionAndPodReplacement(t *testing.T) {
	c, client, _ := fixture(t)
	pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "worker", "namespace": "test", "uid": "pod-a", "resourceVersion": "1", "ownerReferences": []any{map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": "example", "uid": "job-a", "controller": true}}}, "status": map[string]any{"phase": "Running"}}}
	pods := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("test")
	if _, err := pods.Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 0 {
		t.Fatal("successful collection must not hide a missing Pod mapping")
	}
	typed := &corev1.Pod{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pod.Object, typed); err != nil {
		t.Fatal(err)
	}
	c.attributePod(typed)
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 1 {
		t.Fatal("complete fresh attribution must recover")
	}
	if err := pods.Delete(context.Background(), "worker", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	replacement := pod.DeepCopy()
	replacement.SetUID("pod-b")
	if _, err := pods.Create(context.Background(), replacement, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.podInformer.GetStore().Update(replacement); err != nil {
		t.Fatal(err)
	}
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 0 {
		t.Fatal("same Pod name cannot validate attribution from its previous UID")
	}
	c.store.DeletePod("pod-a")
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(replacement.Object, typed); err != nil {
		t.Fatal(err)
	}
	c.attributePod(typed)
	c.verifyHealth(context.Background())
	if workloadHealth(t, c) != 1 {
		t.Fatal("replacement must recover after the current UID is attributed")
	}
}

func TestHealthMissingOptionalChildAPI(t *testing.T) {
	for _, tc := range []struct {
		name               string
		required, observed bool
		want               float64
	}{
		{name: "unused deployment mode", want: 1},
		{name: "explicit requirement", required: true, want: 0},
		{name: "previously observed API", observed: true, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, job := fixture(t)
			entry, ok := c.registry.EntryFor(job.GroupVersionKind().GroupKind())
			if !ok {
				t.Fatal("missing entry")
			}
			definition := entry.Karta.DeepCopy()
			child := schema.GroupVersionKind{Group: "optional.example", Version: "v1", Kind: "Alternative"}
			definition.Spec.StructureDefinition.AdditionalChildKinds = []v1alpha1.GroupVersionKind{{Group: child.Group, Version: child.Version, Kind: child.Kind}}
			c.registry.Set(definition)
			if tc.required {
				c.options.RequiredKinds = []schema.GroupVersionKind{child}
			}
			if tc.observed {
				c.store.SetHealth([]store.HealthRecord{c.kindHealth(child)}, c.store.Snapshot().HealthEpoch)
			}
			c.verifyHealth(t.Context())
			if got := workloadHealth(t, c); got != tc.want {
				t.Fatalf("workload health = %v, want %v", got, tc.want)
			}
		})
	}
}
