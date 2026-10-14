// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func jobOwner(name string, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: uid, Controller: ptr.To(true)}}
}

func putJob(t *testing.T, c *Controller, client *dynamicfake.FakeDynamicClient, job *unstructured.Unstructured) {
	t.Helper()
	jobs := client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace("test")
	if _, err := jobs.Update(t.Context(), job, metav1.UpdateOptions{}); apierrors.IsNotFound(err) {
		if _, err := jobs.Create(t.Context(), job, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err := c.rootWatcherStore(job.GroupVersionKind().GroupKind()).Update(job); err != nil {
		t.Fatal(err)
	}
	c.processWorkload(job, false)
}

func TestZeroPodRootsAndBoundary(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		t.Run(fmt.Sprint(ignored), func(t *testing.T) {
			c, client, child := fixture(t)
			parent := child.DeepCopy()
			parent.SetName("parent")
			parent.SetUID("parent")
			putJob(t, c, client, parent)
			child.SetOwnerReferences(jobOwner("parent", "parent"))
			putJob(t, c, client, child)
			record, _ := c.store.Workload(child.GetUID())
			if record.OwnerResolution != "" {
				t.Fatalf("child exposed status: %+v", record)
			}
			root, _ := c.store.Workload(parent.GetUID())
			if root.OwnerResolution != "resolved" {
				t.Fatalf("zero-Pod root not selected: %+v", root)
			}
			wrapper := schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Wrapper"}
			if ignored {
				c.options.IgnoredOwnerKinds = []schema.GroupVersionKind{wrapper}
			}
			parent.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "example.com/v1", Kind: "Wrapper", Name: "wrapper", UID: "wrapper", Controller: ptr.To(true)}})
			putJob(t, c, client, parent)
			root, _ = c.store.Workload(parent.GetUID())
			expected := ""
			if ignored {
				expected = "ignored_owner"
			}
			if root.OwnerResolution != expected {
				t.Fatalf("root resolution %q want %q", root.OwnerResolution, expected)
			}
			record, _ = c.store.Workload(child.GetUID())
			if record.OwnerResolution != "" {
				t.Fatal("unconfirmed child became root")
			}
			c.verifyHealth(t.Context())
			if ignored {
				found := false
				for _, h := range c.store.Snapshot().Health {
					if h.Scope != "workload" {
						continue
					}
					if h.UID == child.GetUID() {
						t.Fatal("resolved nonroot child emitted workload health")
					}
					if h.UID == parent.GetUID() {
						found = true
						if !h.Healthy {
							t.Fatalf("ignored wrapper poisoned root health: %+v", h)
						}
					}
				}
				if !found {
					t.Fatal("root health missing")
				}
			}
			if !ignored && workloadHealth(t, c) != 0 {
				t.Fatal("unreadable wrapper was healthy")
			}
		})
	}
}

func TestOwnerChangeAndDeletionReattributeDescendant(t *testing.T) {
	c, client, child := fixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "pod", OwnerReferences: jobOwner(child.GetName(), child.GetUID())}}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	if err := c.podInformer.GetStore().Update(pod); err != nil {
		t.Fatal(err)
	}
	c.attributePod(pod)
	parent := child.DeepCopy()
	parent.SetName("parent")
	parent.SetUID("parent")
	putJob(t, c, client, parent)
	child.SetOwnerReferences(jobOwner("parent", "parent"))
	putJob(t, c, client, child)
	mapping, ok := c.store.Pod("pod")
	if !ok || mapping.WorkloadUID != "parent" {
		t.Fatalf("owner transition kept stale mapping: %+v", mapping)
	}
	c.onWorkloadDelete(parent)
	if _, ok := c.store.Pod("pod"); ok {
		t.Fatal("deleting parent kept descendant attribution")
	}
	record, _ := c.store.Workload(child.GetUID())
	if record.OwnerResolution != "" {
		t.Fatal("missing parent published child status")
	}
	putJob(t, c, client, parent)
	mapping, ok = c.store.Pod("pod")
	if !ok || mapping.WorkloadUID != "parent" {
		t.Fatal("parent recovery did not restore descendant")
	}
}

func TestFreshOwnerMissingAndUIDMismatch(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			c, _, job := fixture(t)
			c.options.IgnoredOwnerKinds = []schema.GroupVersionKind{{Group: "batch", Version: "v2", Kind: "Job"}}
			candidate := job.DeepCopy()
			candidate.SetOwnerReferences(jobOwner("gone", "gone"))
			metadata := c.metadataClient.(*metadatafake.FakeMetadataClient)
			reads := 0
			metadata.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				reads++
				if mismatch {
					return true, &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "gone", Namespace: "test", UID: "replacement"}}, nil
				}
				return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, "gone")
			})
			verifier := ownerVerifier{controller: c, objects: map[types.UID]metav1.Object{}, entries: nil, failures: map[types.UID]error{}}
			_, _, err := verifier.root(context.Background(), candidate, job.GetUID())
			if failureReason(err) != store.ReasonUnresolvedOwner || reads != 1 {
				t.Fatalf("owner error=%v reads=%d", err, reads)
			}
			c.options.IgnoredOwnerKinds = []schema.GroupVersionKind{job.GroupVersionKind()}
			uid, resolution, err := verifier.root(t.Context(), candidate, job.GetUID())
			if err != nil || uid != job.GetUID() || resolution != "ignored_owner" || reads != 1 {
				t.Fatalf("boundary did not stop before read: %s %s %v reads=%d", uid, resolution, err, reads)
			}
		})
	}
}

func TestHealthUsesBoundedPages(t *testing.T) {
	c, _, job := fixture(t)
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "1000" {
			t.Errorf("unbounded LIST: %s", r.URL.String())
		}
		result := &unstructured.UnstructuredList{}
		result.SetAPIVersion("v1")
		result.SetKind("List")
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			pages++
			if r.URL.Query().Get("continue") == "" {
				result.Items = []unstructured.Unstructured{*job}
				result.SetContinue("page-2")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.dynamicClient = client
	c.verifyHealth(t.Context())
	if workloadHealth(t, c) != 1 || pages != 2 {
		t.Fatalf("paged verification failed: pages=%d", pages)
	}
}

func TestInvalidDefinitionDoesNotPoisonUnrelatedWorkload(t *testing.T) {
	c, _, job := fixture(t)
	entry, _ := c.registry.EntryFor(job.GroupVersionKind().GroupKind())
	invalid := entry.Karta.DeepCopy()
	invalid.Name = "invalid-other"
	invalid.Spec.StructureDefinition.RootComponent.Kind.Kind = "Broken"
	invalid.Spec.StructureDefinition.RootComponent.Name = ""
	c.registry.Set(invalid)
	c.verifyHealth(t.Context())
	if workloadHealth(t, c) != 1 {
		t.Fatal("invalid unrelated definition poisoned Job")
	}
	found := false
	for _, row := range c.store.Snapshot().Health {
		if row.Ref.Kind == "Broken" && row.Reason == store.ReasonInvalidDefinition && !row.Healthy {
			found = true
		}
	}
	if !found {
		t.Fatal("missing isolated invalid-definition health")
	}
}

func TestSelectedAPIVersionReplacesMappingAndHonorsLifetimeDelete(t *testing.T) {
	c, _, job := fixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "pod", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	if err := c.podInformer.GetStore().Update(pod); err != nil {
		t.Fatal(err)
	}
	c.attributePod(pod)
	entry, _ := c.registry.EntryFor(job.GroupVersionKind().GroupKind())
	definition := entry.Karta.DeepCopy()
	definition.Spec.StructureDefinition.RootComponent.Kind.Version = "v2"
	c.registry.Set(definition)
	next := job.DeepCopy()
	next.SetAPIVersion("batch/v2")
	watcher := c.watchers[job.GroupVersionKind()]
	delete(c.watchers, job.GroupVersionKind())
	watcher.gvk = next.GroupVersionKind()
	c.watchers[watcher.gvk] = watcher
	if err := watcher.informer.GetStore().Update(next); err != nil {
		t.Fatal(err)
	}
	c.processWorkload(next, true)
	actual, ok := c.store.Pod(pod.UID)
	if !ok || actual.Workload.Version != "v2" {
		t.Fatalf("old mapping survived version change: %+v", actual)
	}
	c.onWorkloadDelete(job)
	c.processWorkload(next, true)
	if _, ok := c.store.Workload(job.GetUID()); ok {
		t.Fatal("stale selected-version retry restored the deleted lifetime")
	}
	if _, ok := c.store.Pod(pod.UID); ok {
		t.Fatal("deleted lifetime kept Pod attribution")
	}
	c.retireObservedWorkloadDeletions()
	if _, marked := c.deletedWorkloads.Load(job.GetUID()); !marked {
		t.Fatal("lagging alias lost deletion marker")
	}
	c.mu.Lock()
	for gvk, alias := range c.watchers {
		if gvk.GroupKind() == next.GroupVersionKind().GroupKind() && alias != watcher {
			if alias.stop != nil {
				close(alias.stop)
			}
			delete(c.watchers, gvk)
		}
	}
	c.mu.Unlock()
	if err := watcher.informer.GetStore().Delete(next); err != nil {
		t.Fatal(err)
	}
	c.retireObservedWorkloadDeletions()
	if _, marked := c.deletedWorkloads.Load(job.GetUID()); marked {
		t.Fatal("converged alias retained deletion marker")
	}
}

func TestFullPodCacheEvaluatesCustomResourceSelector(t *testing.T) {
	c, client, job := fixture(t)
	c.options.FullPodCache = true
	entry, _ := c.registry.EntryFor(job.GroupVersionKind().GroupKind())
	definition := entry.Karta.DeepCopy()
	definition.Spec.StructureDefinition.RootComponent.PodSelector = &v1alpha1.PodSelector{ReplicaSelector: &v1alpha1.ReplicaSelector{KeyPath: ".spec.containers[0].resources.requests.cpu"}}
	c.registry.Set(definition)
	c.processWorkload(job, true)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "pod", ResourceVersion: "1", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}}}
	transformed, err := c.podTransform()(pod)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.podInformer.GetStore().Add(transformed); err != nil {
		t.Fatal(err)
	}
	c.onPodEvent(transformed)
	record, ok := c.store.Pod(pod.UID)
	if !ok || record.Component != "job" || record.Replica != "100m" || record.Reason != "" {
		t.Fatalf("full Pod resource selector failed: %+v", record)
	}
	updated := pod.DeepCopy()
	updated.ResourceVersion = "2"
	updated.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("200m")
	if err := c.podInformer.GetStore().Update(updated); err != nil {
		t.Fatal(err)
	}
	c.onPodUpdate(pod, updated)
	record, _ = c.store.Pod(pod.UID)
	if record.Reason != "" || record.Replica != "200m" {
		t.Fatal("spec-only change bypassed full Pod selector")
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(updated)
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{Object: raw}
	obj.SetGroupVersionKind(podGVK)
	if _, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("test").Create(t.Context(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.podInformer.GetStore().Add(updated); err != nil {
		t.Fatal(err)
	}
	c.verifyHealth(t.Context())
	if workloadHealth(t, c) != 1 {
		t.Fatal("full Pod resource selector was not verified after metadata pass")
	}
}

func TestBarePodRequiresExplicitDefinition(t *testing.T) {
	c, client, _ := fixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "test", UID: "bare"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "example"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := c.podInformer.GetStore().Update(pod); err != nil {
		t.Fatal(err)
	}
	c.attributePod(pod)
	if _, ok := c.store.Pod(pod.UID); ok {
		t.Fatal("bare Pod attributed without explicit definition")
	}
	definition, err := catalog.Get(podGVK)
	if err != nil {
		t.Fatal(err)
	}
	c.registry.Set(definition)
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{Object: raw}
	obj.SetGroupVersionKind(podGVK)
	informer := cache.NewSharedIndexInformer(&cache.ListWatch{}, &unstructured.Unstructured{}, 0, cache.Indexers{})
	if err := informer.GetStore().Add(obj); err != nil {
		t.Fatal(err)
	}
	c.watchers[podGVK] = &watcher{gvk: podGVK, isRoot: true, informer: syncedInformer{informer}}
	c.processWorkload(obj, true)
	if err := c.podInformer.GetStore().Update(pod); err != nil {
		t.Fatal(err)
	}
	c.attributePod(pod)
	record, ok := c.store.Pod(pod.UID)
	if !ok || record.WorkloadUID != pod.UID || record.OwnerResolution != "resolved" || record.Reason != "" {
		t.Fatalf("explicit Pod definition did not attribute self: %+v", record)
	}
	if _, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("test").Create(t.Context(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	c.verifyHealth(t.Context())
	found := false
	for _, h := range c.store.Snapshot().Health {
		if h.UID == pod.UID {
			found = true
			if !h.Healthy {
				t.Fatalf("explicit Pod root not freshly verified: %+v", h)
			}
		}
	}
	if !found {
		t.Fatal("explicit Pod root lacks workload health")
	}
}

func TestPendingZeroPodWorkloadRecoversWithoutAnotherOwnerEvent(t *testing.T) {
	c, client, child := fixture(t)
	child.SetOwnerReferences(jobOwner("parent", "parent"))
	putJob(t, c, client, child)
	record, _ := c.store.Workload(child.GetUID())
	if record.OwnerFailure != store.ReasonUnresolvedOwner {
		t.Fatal("missing zero-Pod owner was not recorded")
	}
	// Simulate owner arrival between a failed lookup and publication of failure.
	c.index.UpsertObject("parent", nil)
	c.retryPendingWorkloads()
	record, _ = c.store.Workload(child.GetUID())
	if record.OwnerFailure != "" || record.OwnerResolution != "" {
		t.Fatalf("candidate did not recover as a resolved nonroot: %+v", record)
	}
	found := false
	c.pendingWorkloads.Range(func(_, _ any) bool { found = true; return false })
	if found {
		t.Fatal("recovered candidate is still pending")
	}
}

func TestMetadataEnvelopeUsesWatchedOwnerKind(t *testing.T) {
	c, _, job := fixture(t)
	bridgeGVK := schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Bridge"}
	bridge := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"}, ObjectMeta: metav1.ObjectMeta{Name: "bridge", Namespace: "test", UID: "bridge", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
	cacheChild(t, c, bridge, bridgeGVK)
	c.onChildEvent(bridge, bridgeGVK)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "example.com/v1", Kind: "Bridge", Name: "bridge", UID: "bridge", Controller: ptr.To(true)}}}}
	if err := c.podInformer.GetStore().Update(pod); err != nil {
		t.Fatal(err)
	}
	c.attributePod(pod)
	actual, ok := c.store.Pod(pod.UID)
	if !ok || actual.WorkloadUID != job.GetUID() {
		t.Fatalf("metadata envelope broke owner identity: %+v", actual)
	}
}

func TestFreshOwnerRejectsCrossKindUID(t *testing.T) {
	c, _, job := fixture(t)
	candidate := job.DeepCopy()
	candidate.SetName("candidate")
	candidate.SetUID("candidate")
	refs := jobOwner(job.GetName(), job.GetUID())
	refs[0].Kind = "DifferentKind"
	candidate.SetOwnerReferences(refs)
	verifier := ownerVerifier{controller: c, objects: map[types.UID]metav1.Object{job.GetUID(): job}, entries: map[schema.GroupKind]*registry.Entry{}, failures: map[types.UID]error{}}
	_, _, err := verifier.root(t.Context(), candidate, candidate.GetUID())
	if failureReason(err) != store.ReasonUnresolvedOwner {
		t.Fatalf("cross-kind UID accepted: %v", err)
	}
}

func TestPendingWorkloadRemovedWhenDefinitionDisappears(t *testing.T) {
	c, client, job := fixture(t)
	job.SetOwnerReferences(jobOwner("missing", "missing"))
	putJob(t, c, client, job)
	entry, _ := c.registry.EntryFor(job.GroupVersionKind().GroupKind())
	c.registry.Remove(entry.Karta.Name)
	delete(c.watchers, job.GroupVersionKind())
	c.retryPendingWorkloads()
	found := false
	c.pendingWorkloads.Range(func(_, _ any) bool { found = true; return false })
	if found {
		t.Fatal("unselected workload remained pending after watcher removal")
	}
	if _, exists := c.store.Workload(job.GetUID()); exists {
		t.Fatal("unselected pending workload remained stored")
	}
}

func TestPendingWorkloadSurvivesTemporaryWatcherAbsence(t *testing.T) {
	c, client, job := fixture(t)
	job.SetOwnerReferences(jobOwner("missing", "missing"))
	putJob(t, c, client, job)
	delete(c.watchers, job.GroupVersionKind())
	c.retryPendingWorkloads()
	if _, found := c.pendingWorkloads.Load(job.GetUID()); !found {
		t.Fatal("temporary API watcher failure lost a selected candidate")
	}
}

func TestNotFoundReasonsDistinguishObjectAndCollection(t *testing.T) {
	missing := apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, "missing")
	if got := failureReason(missing); got != store.ReasonUnresolvedOwner {
		t.Fatalf("object 404 reason=%s", got)
	}
	if got := collectionFailureReason(missing); got != store.ReasonAPIUnavailable {
		t.Fatalf("collection 404 reason=%s", got)
	}
	gone := &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: 404}}
	if got := failureReason(gone); got != store.ReasonAPIUnavailable {
		t.Fatalf("missing API endpoint reason=%s", got)
	}
}
