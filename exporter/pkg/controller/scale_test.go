// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/cached/memory"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/test/scale"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
)

type healthScaleFixture struct {
	controller    *Controller
	size          scale.Case
	lists         [3]atomic.Int64
	unexpected    atomic.Int64
	metadataLists atomic.Int64
}

func newHealthScaleFixture(tb testing.TB, size scale.Case) *healthScaleFixture {
	tb.Helper()
	fixture := &healthScaleFixture{size: size}
	jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, total, slot := "", 0, 0
		switch r.URL.Path {
		case "/apis/batch/v1/namespaces/scale/jobs":
			kind, total, slot = "Job", size.Workloads, 0
		case "/api/v1/namespaces/scale/pods":
			kind, total, slot = "Pod", size.Workloads*size.Pods, 1
		case "/apis/run.ai/v1alpha1/kartas":
			kind, total, slot = "Karta", 0, 2
		default:
			fixture.unexpected.Add(1)
			http.Error(w, "unexpected resource or object GET", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodGet || r.URL.Query().Get("limit") != "1000" {
			fixture.unexpected.Add(1)
			http.Error(w, "unbounded or unexpected request", http.StatusBadRequest)
			return
		}
		fixture.lists[slot].Add(1)
		offset := 0
		if token := r.URL.Query().Get("continue"); token != "" {
			var err error
			offset, err = strconv.Atoi(token)
			if err != nil || offset <= 0 || offset >= total {
				fixture.unexpected.Add(1)
				http.Error(w, "invalid continuation", http.StatusBadRequest)
				return
			}
		}
		metadataOnly := strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadataList")
		if metadataOnly {
			fixture.metadataLists.Add(1)
		}
		metadataResult := &metav1.PartialObjectMetadataList{TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadataList"}}
		podResult := &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
		end := min(offset+1000, total)
		result := &unstructured.UnstructuredList{}
		result.SetAPIVersion("v1")
		result.SetKind(kind + "List")
		result.SetResourceVersion("1")
		if end < total {
			result.SetContinue(strconv.Itoa(end))
		}
		for n := offset; n < end; n++ {
			var cached cache.Store
			var key string
			if kind == "Job" {
				cached = fixture.controller.rootWatcherStore(jobGVK.GroupKind())
				key = fmt.Sprintf("scale/job-%d", n)
			} else {
				cached = fixture.controller.podInformer.GetStore()
				key = fmt.Sprintf("scale/pod-%d-%d", n/size.Pods, n%size.Pods)
			}
			item, exists, err := cached.GetByKey(key)
			if err != nil || !exists {
				fixture.unexpected.Add(1)
				http.Error(w, "fixture missing object", http.StatusInternalServerError)
				return
			}
			if metadataOnly {
				object, err := meta.Accessor(item)
				if err != nil {
					fixture.unexpected.Add(1)
					http.Error(w, "fixture metadata failed", http.StatusInternalServerError)
					return
				}
				metadataResult.Items = append(metadataResult.Items, metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: object.GetName(), Namespace: object.GetNamespace(), UID: object.GetUID(), ResourceVersion: object.GetResourceVersion(), OwnerReferences: object.GetOwnerReferences()}})
			} else if object, ok := item.(*unstructured.Unstructured); ok {
				result.Items = append(result.Items, *object)
			} else {
				podResult.Items = append(podResult.Items, *item.(*corev1.Pod))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		var response any = result
		if metadataOnly {
			metadataResult.ListMeta = metav1.ListMeta{ResourceVersion: result.GetResourceVersion(), Continue: result.GetContinue()}
			response = metadataResult
		} else if kind == "Pod" {
			podResult.ListMeta = metav1.ListMeta{ResourceVersion: result.GetResourceVersion(), Continue: result.GetContinue()}
			response = podResult
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			fixture.unexpected.Add(1)
		}
	}))
	tb.Cleanup(server.Close)
	config := &rest.Config{Host: server.URL, QPS: 50, Burst: 100}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		tb.Fatal(err)
	}
	metadataClient, err := metadata.NewForConfig(config)
	if err != nil {
		tb.Fatal(err)
	}
	kube := kubefake.NewClientset()
	discovery := kube.Discovery().(*discoveryfake.FakeDiscovery)
	discovery.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}}},
		{GroupVersion: "batch/v1", APIResources: []metav1.APIResource{{Name: "jobs", Kind: "Job", Namespaced: true}}},
		{GroupVersion: "run.ai/v1alpha1", APIResources: []metav1.APIResource{{Name: "kartas", Kind: "Karta"}}},
	}
	reg := registry.New()
	definition, err := catalog.Get(jobGVK)
	if err != nil {
		tb.Fatal(err)
	}
	reg.Set(definition)
	controller, err := New(client, metadataClient, kube, restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discovery)), reg, owner.New(), store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Namespace: "scale", FullPodCache: true})
	if err != nil {
		tb.Fatal(err)
	}
	fixture.controller = controller
	informer := func(object runtime.Object) cache.SharedIndexInformer {
		return syncedInformer{cache.NewSharedIndexInformer(&cache.ListWatch{}, object, 0, cache.Indexers{})}
	}
	controller.podInformer = informer(&corev1.Pod{})
	controller.kartaInformer = informer(&unstructured.Unstructured{})
	workloadInformer := informer(&unstructured.Unstructured{})
	controller.watchers[jobGVK] = &watcher{gvk: jobGVK, isRoot: true, informer: workloadInformer}
	for n := 0; n < size.Workloads; n++ {
		name := fmt.Sprintf("job-%d", n)
		job := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch/v1", "kind": "Job",
			"metadata": map[string]any{"name": name, "namespace": "scale", "uid": name, "resourceVersion": "1", "generation": int64(1)},
			"spec":     map[string]any{"parallelism": int64(max(1, size.Pods)), "template": map[string]any{"spec": map[string]any{"restartPolicy": "Never", "containers": []any{map[string]any{"name": "worker", "image": "example/worker:v1"}}}}},
			"status":   map[string]any{"active": int64(max(1, size.Pods)), "conditions": []any{}},
		}}
		if err := workloadInformer.GetStore().Add(job); err != nil {
			tb.Fatal(err)
		}
		controller.processWorkload(job, false)
		for p := 0; p < size.Pods; p++ {
			podName := fmt.Sprintf("pod-%d-%d", n, p)
			pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: "scale", UID: types.UID(podName), ResourceVersion: "1", OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: types.UID(name), Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "worker", Image: "example/worker:v1"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			if err := controller.podInformer.GetStore().Add(pod); err != nil {
				tb.Fatal(err)
			}
			controller.attributePod(pod)
		}
	}
	return fixture
}

func (f *healthScaleFixture) verify(tb testing.TB) time.Duration {
	tb.Helper()
	for n := range f.lists {
		f.lists[n].Store(0)
	}
	f.unexpected.Store(0)
	f.metadataLists.Store(0)
	started := time.Now()
	f.controller.verifyHealth(tb.Context())
	elapsed := time.Since(started)
	tb.Logf("verification finished: workloads=%d pods=%d duration=%s lists_jobs=%d lists_pods=%d lists_kartas=%d unexpected_requests=%d", f.size.Workloads, f.size.Workloads*f.size.Pods, elapsed, f.lists[0].Load(), f.lists[1].Load(), f.lists[2].Load(), f.unexpected.Load())
	snapshot := f.controller.store.Snapshot()
	if len(snapshot.Workloads) != f.size.Workloads || len(snapshot.Pods) != f.size.Workloads*f.size.Pods {
		tb.Fatalf("fixture lost observations: workloads=%d pods=%d", len(snapshot.Workloads), len(snapshot.Pods))
	}
	seen := make([]bool, f.size.Workloads)
	kinds := map[string]bool{}
	for _, row := range snapshot.Health {
		if value, reason := row.Status(time.Now()); value != 1 || reason != "none" {
			tb.Fatalf("verification unhealthy after %s: scope=%s kind=%s uid=%s reason=%s", elapsed, row.Scope, row.Ref.Kind, row.UID, reason)
		}
		if row.Scope == "resource_type" {
			if row.Ref.Kind != "Pod" && row.Ref.Kind != "Job" && row.Ref.Kind != "Karta" {
				tb.Fatalf("unexpected collection: %+v", row)
			}
			if kinds[row.Ref.Kind] {
				tb.Fatal("duplicate collection health")
			}
			kinds[row.Ref.Kind] = true
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(string(row.UID), "job-"))
		if err != nil || n < 0 || n >= f.size.Workloads || seen[n] || row.Ref.Name != fmt.Sprintf("job-%d", n) || row.Ref.Namespace != "scale" || row.Ref.Group != "batch" || row.Ref.Version != "v1" || row.Ref.Kind != "Job" {
			tb.Fatalf("invalid workload health identity: %+v", row)
		}
		seen[n] = true
	}
	if len(snapshot.Health) != f.size.Workloads+3 || len(kinds) != 3 {
		tb.Fatalf("health rows=%d want=%d", len(snapshot.Health), f.size.Workloads+3)
	}
	for n, found := range seen {
		if !found {
			tb.Fatalf("missing workload health %d", n)
		}
	}
	pages := func(objects int) int64 { return int64(max(1, (objects+999)/1000)) }
	expected := [3]int64{pages(f.size.Workloads), 2 * pages(f.size.Workloads*f.size.Pods), 1}
	for n, count := range expected {
		if actual := f.lists[n].Load(); actual != count {
			tb.Fatalf("LIST count[%d]=%d want=%d", n, actual, count)
		}
	}
	if actual := f.metadataLists.Load(); actual != pages(f.size.Workloads*f.size.Pods) {
		tb.Fatalf("metadata LISTs=%d want=%d", actual, pages(f.size.Workloads*f.size.Pods))
	}
	if f.unexpected.Load() != 0 {
		tb.Fatalf("unexpected/unbounded API requests: %d", f.unexpected.Load())
	}
	tb.Logf("workloads=%d pods=%d health_rows=%d healthy=%d lists_jobs=%d lists_pods=%d lists_kartas=%d fallback_gets=0 duration=%s", f.size.Workloads, f.size.Workloads*f.size.Pods, len(snapshot.Health), len(snapshot.Health), expected[0], expected[1], expected[2], elapsed)
	return elapsed
}

func healthScaleCases(tb testing.TB) []scale.Case {
	tb.Helper()
	workloads := os.Getenv("KARTA_SCALE_WORKLOADS")
	if workloads == "" {
		tb.Skip("set KARTA_SCALE_WORKLOADS and optionally KARTA_SCALE_PODS_PER_WORKLOAD to run scale verification")
	}
	cases, err := scale.Cases(workloads, os.Getenv("KARTA_SCALE_PODS_PER_WORKLOAD"))
	if err != nil {
		tb.Fatal(err)
	}
	return cases
}

func TestScaleHealthVerification(t *testing.T) {
	for _, size := range healthScaleCases(t) {
		t.Run(size.Name(), func(t *testing.T) { newHealthScaleFixture(t, size).verify(t) })
	}
}

func BenchmarkScaleHealthVerification(b *testing.B) {
	for _, size := range healthScaleCases(b) {
		b.Run(size.Name(), func(b *testing.B) {
			fixture := newHealthScaleFixture(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				fixture.verify(b)
			}
			b.ReportMetric(float64(fixture.lists[0].Load()+fixture.lists[1].Load()+fixture.lists[2].Load()), "LISTs/op")
		})
	}
}

func TestHealthMetadataPassPreservesPodOwners(t *testing.T) {
	f := newHealthScaleFixture(t, scale.Case{Workloads: 2, Pods: 1})
	item, exists, err := f.controller.podInformer.GetStore().GetByKey("scale/pod-1-0")
	if err != nil || !exists {
		t.Fatal("missing child Pod fixture")
	}
	child := item.(*corev1.Pod).DeepCopy()
	child.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "pod-0-0", UID: "pod-0-0", Controller: ptr.To(true)}}
	if err := f.controller.podInformer.GetStore().Update(child); err != nil {
		t.Fatal(err)
	}
	f.controller.attributePod(child)
	if actual, ok := f.controller.store.Pod(child.UID); !ok || actual.WorkloadUID != "job-0" {
		t.Fatalf("nested Pod not reattributed: %+v", actual)
	}
	f.verify(t)
}
