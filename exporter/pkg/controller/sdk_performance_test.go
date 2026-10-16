// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/attribute"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/telemetry"
	"github.com/dsx-ai-factory/workload-map/exporter/test/scale"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
)

type sdkConsumerFixture struct {
	controller *Controller
	entry      *registry.Entry
	workloads  []*unstructured.Unstructured
	pods       []*corev1.Pod
}

var sdkConsumerRootGVK = schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}
var sdkConsumerJobGVK = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

func sdkConsumerOwner(gvk schema.GroupVersionKind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: name, UID: types.UID(name), Controller: ptr.To(true)}}
}

func newSDKConsumerFixture(tb testing.TB, count int) *sdkConsumerFixture {
	tb.Helper()
	reg := registry.New()
	karta, err := catalog.Get(sdkConsumerRootGVK)
	if err != nil {
		tb.Fatal(err)
	}
	reg.Set(karta)
	entry, ok := reg.EntryFor(sdkConsumerRootGVK.GroupKind())
	if !ok {
		tb.Fatal("JobSet definition missing")
	}
	c, err := New(nil, nil, nil, nil, reg, owner.New(), store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	if err != nil {
		tb.Fatal(err)
	}
	c.podInformer = cache.NewSharedIndexInformer(&cache.ListWatch{}, &corev1.Pod{}, 0, cache.Indexers{})
	roots := cache.NewSharedIndexInformer(&cache.ListWatch{}, &unstructured.Unstructured{}, 0, cache.Indexers{})
	c.watchers[sdkConsumerRootGVK] = &watcher{gvk: sdkConsumerRootGVK, isRoot: true, informer: roots}
	f := &sdkConsumerFixture{controller: c, entry: entry}
	for n := range count {
		name := fmt.Sprintf("jobset-%d", n)
		workload := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": sdkConsumerRootGVK.GroupVersion().String(), "kind": sdkConsumerRootGVK.Kind,
			"metadata": map[string]any{"name": name, "namespace": "performance", "uid": name, "resourceVersion": "1", "generation": int64(1)},
			"spec": map[string]any{"replicatedJobs": []any{
				map[string]any{"name": "prefill", "replicas": int64(1), "template": map[string]any{"spec": map[string]any{"parallelism": int64(1)}}},
				map[string]any{"name": "decode", "replicas": int64(1), "template": map[string]any{"spec": map[string]any{"parallelism": int64(1)}}},
			}},
			"status": map[string]any{"conditions": []any{}, "replicatedJobsStatus": []any{
				map[string]any{"name": "prefill", "active": int64(1), "ready": int64(1), "failed": int64(0)},
				map[string]any{"name": "decode", "active": int64(1), "ready": int64(1), "failed": int64(0)},
			}},
		}}
		if err := roots.GetStore().Add(workload); err != nil {
			tb.Fatal(err)
		}
		c.processWorkload(workload, false)
		f.workloads = append(f.workloads, workload)
		for p, instance := range []string{"prefill", "decode"} {
			jobName := fmt.Sprintf("job-%d-%d", n, p)
			job := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "performance", UID: types.UID(jobName), OwnerReferences: sdkConsumerOwner(sdkConsumerRootGVK, name)}}
			c.index.UpsertTrackedObject(job, sdkConsumerJobGVK)
			podName := fmt.Sprintf("pod-%d-%d", n, p)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: "performance", UID: types.UID(podName), ResourceVersion: "1", OwnerReferences: sdkConsumerOwner(sdkConsumerJobGVK, jobName), Labels: map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": instance, "jobset.sigs.k8s.io/job-index": "0"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			if err := c.podInformer.GetStore().Add(pod); err != nil {
				tb.Fatal(err)
			}
			c.attributePod(pod)
			f.pods = append(f.pods, pod)
		}
	}
	return f
}

func (f *sdkConsumerFixture) verify(tb testing.TB) {
	tb.Helper()
	snapshot := f.controller.store.Snapshot()
	if len(snapshot.Workloads) != len(f.workloads) || len(snapshot.Pods) != len(f.pods) {
		tb.Fatalf("wrong fixture counts: workloads=%d pods=%d", len(snapshot.Workloads), len(snapshot.Pods))
	}
	for _, workload := range f.workloads {
		record, ok := f.controller.store.Workload(workload.GetUID())
		if !ok || record.EvaluationFailed || record.OwnerFailure != "" || record.OwnerResolution != "resolved" || len(record.Components) != 2 {
			tb.Fatalf("invalid workload record: %+v", record)
		}
		ids, err := attribute.InstanceIDs(tb.Context(), f.entry, workload)
		if err != nil || !reflect.DeepEqual(ids, map[string][]string{"replicatedjob": {"prefill", "decode"}}) {
			tb.Fatalf("wrong instance IDs: %v, %v", ids, err)
		}
	}
	for p, pod := range f.pods {
		record, ok := f.controller.store.Pod(pod.UID)
		if !ok || record.WorkloadUID != f.workloads[p/2].GetUID() || record.Component != "replicatedjob" || record.Instance != []string{"prefill", "decode"}[p%2] || record.Replica != "0" || record.Reason != "" || record.OwnerResolution != "resolved" {
			tb.Fatalf("invalid Pod record: %+v", record)
		}
	}
}

func (f *sdkConsumerFixture) handler(tb testing.TB) http.Handler {
	tb.Helper()
	reg := prometheus.NewRegistry()
	provider, err := telemetry.NewPrometheusProvider(reg)
	if err != nil {
		tb.Fatal(err)
	}
	registration, err := collector.New(provider.Meter("consumer-performance"), f.controller.store, collector.Options{})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = registration.Unregister(); _ = provider.Shutdown(context.Background()) })
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

func sdkConsumerDigest(tb testing.TB, handler http.Handler) (string, int) {
	tb.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, scale.Request())
	if recorder.Code != http.StatusOK {
		tb.Fatalf("HTTP %d", recorder.Code)
	}
	lines := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n")
	slices.Sort(lines)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(lines, "\n")))), len(lines)
}

func TestSDKConsumerParity(t *testing.T) {
	f := newSDKConsumerFixture(t, 32)
	f.verify(t)
	handler := f.handler(t)
	initial, lines := sdkConsumerDigest(t, handler)
	for _, workload := range f.workloads {
		f.controller.processWorkload(workload, true)
	}
	for _, pod := range f.pods {
		f.controller.attributePod(pod)
	}
	f.verify(t)
	after, _ := sdkConsumerDigest(t, handler)
	if initial != after {
		t.Fatal("same-state replay changed the exposition")
	}
	unknown := f.pods[0].DeepCopy()
	unknown.Labels["jobset.sigs.k8s.io/replicatedjob-name"] = "absent"
	result := attribute.Attribute(t.Context(), unknown, f.entry, map[string][]string{"replicatedjob": {"prefill", "decode"}})
	if result.Instance != collector.SentinelUnknown || result.Reason != collector.ReasonUnknownInstance {
		t.Fatalf("unknown instance contract changed: %+v", result)
	}
	missing := f.controller.index.Resolve(sdkConsumerOwner(sdkConsumerJobGVK, "missing"), f.controller.registry.IsRoot, nil, nil, "performance")
	if missing.Outcome != owner.OutcomeMissing || missing.Missing != "missing" {
		t.Fatalf("missing owner contract changed: %+v", missing)
	}
	t.Logf("parity workloads=32 pods=64 exposition_lines=%d sha256=%s", lines, initial)
}

func TestSDKConsumerConcurrentEvents(t *testing.T) {
	f := newSDKConsumerFixture(t, 8)
	var workers sync.WaitGroup
	for worker := range 3 {
		workers.Go(func() {
			for n := range 24 {
				switch worker {
				case 0:
					f.controller.attributePod(f.pods[n%len(f.pods)])
				case 1:
					f.controller.processWorkload(f.workloads[n%len(f.workloads)], false)
				case 2:
					f.controller.index.Resolve(f.pods[n%len(f.pods)].OwnerReferences, f.controller.registry.IsRoot, nil, nil, "performance")
				}
			}
		})
	}
	workers.Wait()
	f.verify(t)
}

// Each operation uses the real exporter path. The same file is compiled with
// the historical two-file overlay to isolate the shared-algorithm extraction.
func BenchmarkSDKConsumer(b *testing.B) {
	for _, name := range []string{"OwnerWarm", "OwnerUpdate", "InstanceIDs", "PodAttribution", "PodEvent", "WorkloadUpdate", "DefinitionReattribute", "Scrape"} {
		b.Run(name, func(b *testing.B) {
			f := newSDKConsumerFixture(b, 1000)
			ids := map[string][]string{"replicatedjob": {"prefill", "decode"}}
			var handler http.Handler
			var bytes int64
			if name == "Scrape" {
				handler = f.handler(b)
				response := &scale.Response{}
				handler.ServeHTTP(response, scale.Request())
				if response.Code != http.StatusOK {
					b.Fatal("warm scrape failed")
				}
				bytes = response.Bytes
			}
			job := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "job-0-0", Namespace: "performance", UID: "job-0-0", OwnerReferences: sdkConsumerOwner(sdkConsumerRootGVK, "jobset-0")}}
			roots := f.controller.rootWatcherStore(sdkConsumerRootGVK.GroupKind())
			// Both published versions remain immutable, as informer objects must be.
			changed := f.workloads[0].DeepCopy()
			changed.SetResourceVersion("2")
			changed.SetGeneration(2)
			versions := [2]*unstructured.Unstructured{f.workloads[0], changed}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				pod := f.pods[n%len(f.pods)]
				switch name {
				case "OwnerWarm":
					result := f.controller.index.Resolve(pod.OwnerReferences, f.controller.registry.IsRoot, nil, nil, pod.Namespace)
					if result.Outcome != owner.OutcomeFound {
						b.Fatal(result)
					}
				case "OwnerUpdate":
					job.OwnerReferences[0].Name = f.workloads[n%2].GetName()
					job.OwnerReferences[0].UID = f.workloads[n%2].GetUID()
					f.controller.index.UpsertTrackedObject(job, sdkConsumerJobGVK)
					result := f.controller.index.Resolve(f.pods[0].OwnerReferences, f.controller.registry.IsRoot, nil, nil, "performance")
					if result.Root.UID != job.OwnerReferences[0].UID {
						b.Fatal(result)
					}
				case "InstanceIDs":
					result, err := attribute.InstanceIDs(b.Context(), f.entry, f.workloads[n%len(f.workloads)])
					if err != nil || len(result["replicatedjob"]) != 2 {
						b.Fatalf("instance IDs: %v, %v", result, err)
					}
				case "PodAttribution":
					result := attribute.Attribute(b.Context(), pod, f.entry, ids)
					if result.Reason != "" || result.Instance == "" {
						b.Fatal(result)
					}
				case "PodEvent":
					f.controller.attributePod(pod)
				case "WorkloadUpdate":
					workload := versions[n%2]
					if err := roots.Update(workload); err != nil {
						b.Fatal(err)
					}
					f.controller.processWorkload(workload, false)
				case "DefinitionReattribute":
					f.controller.processWorkload(f.workloads[n%len(f.workloads)], true)
				case "Scrape":
					response := &scale.Response{}
					handler.ServeHTTP(response, scale.Request())
					if response.Code != http.StatusOK || response.Bytes != bytes {
						b.Fatalf("scrape HTTP=%d bytes=%d expected=%d", response.Code, response.Bytes, bytes)
					}
				}
			}
			b.StopTimer()
			if name == "Scrape" {
				b.SetBytes(bytes)
				b.ReportMetric(float64(bytes), "exposition-B/op")
			}
			b.ReportMetric(1000, "workloads")
			b.ReportMetric(2000, "pods")
			runtime.KeepAlive(f)
		})
	}
}

func sdkConsumerIndex(count int) *owner.Index {
	index := owner.New()
	for n := range count {
		rootName, jobName, podName := fmt.Sprintf("jobset-%d", n), fmt.Sprintf("job-%d", n), fmt.Sprintf("pod-%d", n)
		root := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: rootName, Namespace: "performance", UID: types.UID(rootName)}}
		job := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "performance", UID: types.UID(jobName), OwnerReferences: sdkConsumerOwner(sdkConsumerRootGVK, rootName)}}
		pod := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: "performance", UID: types.UID(podName), OwnerReferences: sdkConsumerOwner(sdkConsumerJobGVK, jobName)}}
		index.UpsertTrackedObject(root, sdkConsumerRootGVK)
		index.UpsertTrackedObject(job, sdkConsumerJobGVK)
		index.UpsertTrackedObject(pod, podGVK)
	}
	return index
}

// A separate process per size keeps cumulative GC and peak RSS attributable.
func TestSDKConsumerIndexMemory(t *testing.T) {
	value := os.Getenv("KARTA_PERF_SIZE")
	if value == "" {
		t.Skip("set KARTA_PERF_SIZE=10000 or 100000 for retained-index measurement")
	}
	size, err := strconv.Atoi(value)
	if err != nil || (size != 10000 && size != 100000) {
		t.Fatal("KARTA_PERF_SIZE must be 10000 or 100000")
	}
	runtime.GC()
	var before, built, warmed runtime.MemStats
	runtime.ReadMemStats(&before)
	index := sdkConsumerIndex(size)
	runtime.GC()
	runtime.ReadMemStats(&built)
	for n := range size {
		jobName, rootName := fmt.Sprintf("job-%d", n), fmt.Sprintf("jobset-%d", n)
		if n%10 == 0 {
			job := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "performance", UID: types.UID(jobName), OwnerReferences: sdkConsumerOwner(sdkConsumerRootGVK, rootName)}}
			index.UpsertTrackedObject(job, sdkConsumerJobGVK)
		}
		result := index.Resolve(sdkConsumerOwner(sdkConsumerJobGVK, jobName), func(gk schema.GroupKind) bool { return gk == sdkConsumerRootGVK.GroupKind() }, nil, nil, "performance")
		if result.Outcome != owner.OutcomeFound || result.Root.UID != types.UID(rootName) {
			t.Fatalf("root lookup failed: %+v", result)
		}
	}
	runtime.ReadMemStats(&warmed)
	warmGC, warmAlloc, warmMallocs := warmed.NumGC-built.NumGC, warmed.TotalAlloc-built.TotalAlloc, warmed.Mallocs-built.Mallocs
	warmPause := warmed.PauseTotalNs - built.PauseTotalNs
	runtime.GC()
	runtime.ReadMemStats(&warmed)
	report := map[string]any{
		"workloads": size, "index_nodes": 3 * size, "warm_lookups": size, "warm_metadata_updates": size / 10,
		"gomaxprocs": runtime.GOMAXPROCS(0), "go_version": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH,
		"empty_heap_bytes": before.HeapAlloc, "built_heap_bytes": built.HeapAlloc, "built_heap_delta_bytes": int64(built.HeapAlloc) - int64(before.HeapAlloc),
		"warm_heap_bytes": warmed.HeapAlloc, "warm_heap_delta_bytes": int64(warmed.HeapAlloc) - int64(before.HeapAlloc),
		"build_allocated_bytes": built.TotalAlloc - before.TotalAlloc, "build_allocations": built.Mallocs - before.Mallocs,
		"build_gc_cycles_including_forced": built.NumGC - before.NumGC, "warm_allocated_bytes": warmAlloc, "warm_allocations": warmMallocs, "warm_gc_cycles": warmGC,
		"warm_gc_pause_ns": warmPause,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("KARTA_PERF_REPORT"); path != "" {
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Log(string(data))
	}
	runtime.KeepAlive(index)
}
