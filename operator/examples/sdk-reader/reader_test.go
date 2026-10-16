// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package sdkreader

import (
	"context"
	"errors"
	"fmt"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type stubReader struct {
	get  func(context.Context, client.ObjectKey, client.Object) error
	list func(context.Context, client.ObjectList, client.ListOptions) error
}

type stubMapper struct {
	meta.RESTMapper
	mapping *meta.RESTMapping
}

var jobGVK = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

func (s *stubReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, _ ...client.GetOption) error {
	return s.get(ctx, key, object)
}

func (s *stubReader) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	parsed := client.ListOptions{}
	parsed.ApplyOptions(options)
	return s.list(ctx, list, parsed)
}

func testMapper() meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{podGVK.GroupVersion(), jobGVK.GroupVersion()})
	mapper.Add(podGVK, meta.RESTScopeNamespace)
	mapper.Add(jobGVK, meta.RESTScopeNamespace)
	return mapper
}

func testPod(name string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "demo", UID: types.UID(name), Labels: map[string]string{"test": "original"}}, Status: corev1.PodStatus{Phase: phase}}
}

func TestAPIReaderExhaustsPagesWithoutFilteringPhases(t *testing.T) {
	pages := []corev1.PodList{
		{ListMeta: metav1.ListMeta{ResourceVersion: "42", Continue: "next"}, Items: []corev1.Pod{testPod("running", corev1.PodRunning)}},
		{ListMeta: metav1.ListMeta{ResourceVersion: "42"}, Items: []corev1.Pod{testPod("done", corev1.PodSucceeded), testPod("failed", corev1.PodFailed)}},
	}
	calls := 0
	source := &stubReader{list: func(_ context.Context, list client.ObjectList, options client.ListOptions) error {
		if options.Namespace != "demo" || options.Limit != 500 || options.LabelSelector != nil || options.FieldSelector != nil {
			t.Fatalf("unexpected list options: %+v", options)
		}
		if (calls == 0 && options.Continue != "") || (calls == 1 && options.Continue != "next") {
			t.Fatalf("unexpected continuation: %q", options.Continue)
		}
		*list.(*corev1.PodList) = pages[calls]
		calls++
		return nil
	}}
	reader, err := NewAPIReader(source, testMapper())
	if err != nil {
		t.Fatal(err)
	}
	pods, err := reader.ListPods(context.Background(), "demo")
	if err != nil || calls != 2 || len(pods.Items) != 3 || pods.Continue != "" || pods.ResourceVersion != "42" {
		t.Fatalf("pods=%+v calls=%d error=%v", pods, calls, err)
	}
	pods.Items[0].Labels["test"] = "changed"
	if pages[0].Items[0].Labels["test"] != "original" {
		t.Fatal("returned Pod aliases the source")
	}
}

func TestAPIReaderRejectsBrokenPagination(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages []corev1.PodList
	}{
		{name: "missing resource version", pages: []corev1.PodList{{ListMeta: metav1.ListMeta{Continue: "next"}}}},
		{name: "resource version changed", pages: []corev1.PodList{{ListMeta: metav1.ListMeta{ResourceVersion: "42", Continue: "next"}}, {ListMeta: metav1.ListMeta{ResourceVersion: "43"}}}},
		{name: "repeated token", pages: []corev1.PodList{{ListMeta: metav1.ListMeta{ResourceVersion: "42", Continue: "next"}}, {ListMeta: metav1.ListMeta{ResourceVersion: "42", Continue: "next"}}}},
		{name: "cache used as API reader", pages: []corev1.PodList{{ListMeta: metav1.ListMeta{Continue: "continue-not-supported"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			source := &stubReader{list: func(_ context.Context, list client.ObjectList, _ client.ListOptions) error {
				*list.(*corev1.PodList) = tc.pages[calls]
				calls++
				return nil
			}}
			reader, _ := NewAPIReader(source, testMapper())
			pods, err := reader.ListPods(context.Background(), "demo")
			if pods != nil || !errors.Is(err, discovery.ErrIncompleteOwnership) {
				t.Fatalf("pods=%+v error=%v", pods, err)
			}
		})
	}
}

func TestCacheReaderRequestsCompleteListWithoutPagination(t *testing.T) {
	page := corev1.PodList{ListMeta: metav1.ListMeta{Continue: "continue-not-supported"}}
	for i := range 601 {
		page.Items = append(page.Items, testPod(fmt.Sprintf("pod-%d", i), corev1.PodRunning))
	}
	calls := 0
	source := &stubReader{list: func(_ context.Context, list client.ObjectList, options client.ListOptions) error {
		if options.Limit != 0 || options.Continue != "" || options.LabelSelector != nil || options.FieldSelector != nil {
			t.Fatalf("cache list must be unfiltered and unlimited: %+v", options)
		}
		calls++
		*list.(*corev1.PodList) = page
		return nil
	}}
	reader, _ := NewCacheReader(source, testMapper(), "demo", func() bool { return true })
	pods, err := reader.ListPods(context.Background(), "demo")
	if err != nil || calls != 1 || len(pods.Items) != 601 || pods.Continue != "" {
		t.Fatalf("pods=%+v calls=%d error=%v", pods, calls, err)
	}
	pods.Items[0].Labels["test"] = "changed"
	if page.Items[0].Labels["test"] != "original" {
		t.Fatal("returned cached Pod aliases the source")
	}
}

func TestCacheReaderRejectsIncompleteScopeAndReadiness(t *testing.T) {
	ready := false
	calls := 0
	source := &stubReader{list: func(_ context.Context, _ client.ObjectList, _ client.ListOptions) error {
		calls++
		ready = false
		return nil
	}}
	reader, _ := NewCacheReader(source, testMapper(), "demo", func() bool { return ready })
	if _, err := reader.ListPods(context.Background(), "demo"); !errors.Is(err, discovery.ErrNotSynced) || calls != 0 {
		t.Fatalf("unready cache was read: calls=%d error=%v", calls, err)
	}
	if _, err := reader.Mapping(context.Background(), podGVK); !errors.Is(err, discovery.ErrNotSynced) {
		t.Fatalf("unready mapping error: %v", err)
	}
	ready = true
	for _, namespace := range []string{"other", ""} {
		if _, err := reader.ListPods(context.Background(), namespace); !errors.Is(err, discovery.ErrInsufficientScope) || calls != 0 {
			t.Fatalf("scope %q: calls=%d error=%v", namespace, calls, err)
		}
	}
	if _, err := reader.Get(context.Background(), discovery.ObjectRef{GVK: podGVK, Namespace: "other", Name: "pod", UID: "pod"}); !errors.Is(err, discovery.ErrInsufficientScope) {
		t.Fatalf("out-of-scope Get: %v", err)
	}
	if _, err := reader.ListPods(context.Background(), "demo"); !errors.Is(err, discovery.ErrNotSynced) || calls != 1 {
		t.Fatalf("readiness lost during read: calls=%d error=%v", calls, err)
	}
}

func TestGetValidatesIdentityAndDetachesObjects(t *testing.T) {
	job := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "train", "namespace": "demo", "uid": "job-uid", "labels": map[string]any{"test": "original"}}}}
	pod := testPod("pod", corev1.PodRunning)
	source := &stubReader{get: func(_ context.Context, _ client.ObjectKey, object client.Object) error {
		switch target := object.(type) {
		case *corev1.Pod:
			*target = pod
		case *unstructured.Unstructured:
			if target.GroupVersionKind() != jobGVK {
				t.Fatalf("requested GVK is missing: %s", target.GroupVersionKind())
			}
			*target = *job
		default:
			t.Fatalf("unexpected object type %T", object)
		}
		return nil
	}}
	reader, _ := NewAPIReader(source, testMapper())
	ref := discovery.ObjectRef{GVK: jobGVK, Namespace: "demo", Name: "train", UID: "job-uid"}
	got, err := reader.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	got.Object["metadata"].(map[string]any)["labels"].(map[string]any)["test"] = "changed"
	if job.GetLabels()["test"] != "original" {
		t.Fatal("Get result aliases the source")
	}
	ref.UID = "old-uid"
	if _, err := reader.Get(context.Background(), ref); !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatalf("stale UID accepted: %v", err)
	}
	podObject, err := reader.Get(context.Background(), discovery.PodReference(&pod))
	if err != nil || podObject.GroupVersionKind() != podGVK {
		t.Fatalf("typed Pod without TypeMeta: object=%v error=%v", podObject, err)
	}
	podObject.Object["metadata"].(map[string]any)["labels"].(map[string]any)["test"] = "changed"
	if pod.Labels["test"] != "original" {
		t.Fatal("converted Pod aliases the source")
	}
}

func TestReaderPropagatesCancellationAndSourceErrors(t *testing.T) {
	failure := errors.New("source failed")
	ctx, cancel := context.WithCancel(context.Background())
	source := &stubReader{
		get:  func(context.Context, client.ObjectKey, client.Object) error { return failure },
		list: func(context.Context, client.ObjectList, client.ListOptions) error { return failure },
	}
	reader, _ := NewAPIReader(source, testMapper())
	if _, err := reader.Get(ctx, discovery.ObjectRef{GVK: jobGVK, Namespace: "demo", Name: "train", UID: "uid"}); !errors.Is(err, failure) {
		t.Fatalf("Get error: %v", err)
	}
	if _, err := reader.ListPods(ctx, "demo"); !errors.Is(err, failure) {
		t.Fatalf("List error: %v", err)
	}
	cancel()
	if _, err := reader.ListPods(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list: %v", err)
	}
	if _, err := reader.Mapping(ctx, jobGVK); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mapping: %v", err)
	}
}

func TestReaderRejectsBadPodLists(t *testing.T) {
	for _, tc := range []struct {
		name string
		page corev1.PodList
		want error
	}{
		{name: "duplicate UID", page: corev1.PodList{Items: []corev1.Pod{testPod("same", corev1.PodRunning), testPod("same", corev1.PodRunning)}}, want: discovery.ErrInvalidInput},
		{name: "missing identity", page: corev1.PodList{Items: []corev1.Pod{{}}}, want: discovery.ErrInvalidInput},
		{name: "outside namespace", page: corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "pod", UID: "pod", Namespace: "other"}}}}, want: discovery.ErrInsufficientScope},
		{name: "unknown cache pagination", page: corev1.PodList{ListMeta: metav1.ListMeta{Continue: "unexpected"}}, want: discovery.ErrIncompleteOwnership},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &stubReader{list: func(_ context.Context, list client.ObjectList, _ client.ListOptions) error {
				*list.(*corev1.PodList) = tc.page
				return nil
			}}
			reader, _ := NewCacheReader(source, testMapper(), "demo", func() bool { return true })
			if _, err := reader.ListPods(context.Background(), "demo"); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestReaderConstructorsRejectMissingDependencies(t *testing.T) {
	var source *stubReader
	if _, err := NewAPIReader(source, testMapper()); !errors.Is(err, discovery.ErrNoClient) {
		t.Fatalf("typed nil client: %v", err)
	}
	if _, err := NewAPIReader(&stubReader{}, nil); !errors.Is(err, discovery.ErrNoClient) {
		t.Fatalf("nil mapper: %v", err)
	}
	if _, err := NewCacheReader(&stubReader{}, testMapper(), "", nil); !errors.Is(err, discovery.ErrNoClient) {
		t.Fatalf("nil readiness: %v", err)
	}
}

func (m *stubMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return m.mapping, nil
}

func TestMappingReturnsDetachedExactVersion(t *testing.T) {
	original, err := testMapper().RESTMapping(jobGVK.GroupKind(), jobGVK.Version)
	if err != nil {
		t.Fatal(err)
	}
	mapper := &stubMapper{mapping: original}
	reader, _ := NewAPIReader(&stubReader{}, mapper)
	got, err := reader.Mapping(context.Background(), jobGVK)
	if err != nil {
		t.Fatal(err)
	}
	got.Resource.Resource = "changed"
	if original.Resource.Resource == "changed" {
		t.Fatal("mapping aliases the mapper")
	}
	mapper.mapping = &meta.RESTMapping{GroupVersionKind: schema.GroupVersionKind{Group: "batch", Version: "v2", Kind: "Job"}, Resource: original.Resource, Scope: original.Scope}
	if _, err := reader.Mapping(context.Background(), jobGVK); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("wrong version accepted: %v", err)
	}
}

func TestReaderRejectsCancellationDuringRead(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := &stubReader{list: func(context.Context, client.ObjectList, client.ListOptions) error { cancel(); return nil }}
		reader, _ := NewAPIReader(source, testMapper())
		if _, err := reader.ListPods(ctx, "demo"); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled list returned success: %v", err)
		}
	})
	t.Run("get", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pod := testPod("pod", corev1.PodRunning)
		source := &stubReader{get: func(_ context.Context, _ client.ObjectKey, object client.Object) error {
			*object.(*corev1.Pod) = pod
			cancel()
			return nil
		}}
		reader, _ := NewAPIReader(source, testMapper())
		if _, err := reader.Get(ctx, discovery.PodReference(&pod)); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Get returned success: %v", err)
		}
	})
}

func TestPodToWorkloadWithRuntimeClient(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	controller := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "train", Namespace: "demo", UID: "job-uid"}}
	job.Spec.Template.Spec.Containers = []corev1.Container{{Name: "trainer", Image: "example/trainer:v1"}}
	pod := testPod("training-pod", corev1.PodRunning)
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}}
	other := testPod("unrelated", corev1.PodSucceeded)
	source := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, &pod, &other).Build()
	reader, err := NewAPIReader(source, testMapper())
	if err != nil {
		t.Fatal(err)
	}
	discoverer, err := karta.NewDiscoverer(reader, discovery.Options{MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := discoverer.WorkloadForPod(context.Background(), discovery.PodReference(&pod))
	if err != nil {
		t.Fatal(err)
	}
	if observed.Root().GVK != jobGVK || observed.Root().UID != job.UID || len(observed.Pods()) != 1 || observed.Pods()[0].Name != pod.Name {
		t.Fatalf("unexpected workload: root=%+v pods=%+v", observed.Root(), observed.Pods())
	}
}
