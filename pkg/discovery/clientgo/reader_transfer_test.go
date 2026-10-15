// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package clientgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

type podTransferFixture struct {
	reader      discovery.Reader
	recorder    *podTransferDynamic
	pods        []corev1.Pod
	pages       map[string][]byte
	seed        discovery.ObjectRef
	root        discovery.ObjectRef
	rootObject  *unstructured.Unstructured
	gets, lists int
	rootCount   int
}

type podTransferDynamic struct {
	dynamic.Interface
	pages   []*unstructured.UnstructuredList
	objects []*unstructured.Unstructured
}

type podTransferResource struct {
	dynamic.NamespaceableResourceInterface
	recorder *podTransferDynamic
}

type podTransferScopedResource struct {
	dynamic.ResourceInterface
	recorder *podTransferDynamic
}

var podTransferGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}

func newPodTransferFixture(tb testing.TB, count, fanout int, record bool) *podTransferFixture {
	tb.Helper()
	f := &podTransferFixture{pages: make(map[string][]byte), rootCount: (count + fanout - 1) / fanout}
	ownerGVK := schema.GroupVersionKind{Group: "transfer.example", Version: "v1", Kind: "Workload"}
	responses := make(map[string][]byte)
	for i := range f.rootCount {
		name := fmt.Sprintf("root-%06d", i)
		root := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": ownerGVK.GroupVersion().String(), "kind": ownerGVK.Kind,
			"metadata": map[string]any{"name": name, "namespace": "team", "uid": name, "resourceVersion": "1"},
			"spec":     map[string]any{"payload": []any{"complete", map[string]any{"value": "root-body"}}},
		}}
		body, err := json.Marshal(root)
		if err != nil {
			tb.Fatal(err)
		}
		responses["/apis/transfer.example/v1/namespaces/team/workloads/"+name] = body
		if i == 0 {
			f.rootObject = root
			f.root = discovery.ObjectRef{GVK: ownerGVK, Namespace: "team", Name: name, UID: types.UID(name)}
		}
	}
	for i := range count {
		name := fmt.Sprintf("pod-%06d", i)
		owner := fmt.Sprintf("root-%06d", i/fanout)
		pod := podTransferPod(name, owner, ownerGVK)
		body, err := json.Marshal(pod)
		if err != nil {
			tb.Fatal(err)
		}
		if i == 0 {
			responses["/api/v1/namespaces/team/pods/"+name] = body
			f.seed = discovery.ObjectRef{GVK: podTransferGVK, Namespace: "team", Name: name, UID: types.UID(name)}
		}
		var expected corev1.Pod
		if err := json.Unmarshal(body, &expected); err != nil {
			tb.Fatal(err)
		}
		f.pods = append(f.pods, expected)
	}
	pageSize := min(podPageSize, max(count/2, 1))
	for offset := 0; offset < count; offset += pageSize {
		end := min(offset+pageSize, count)
		token := ""
		if offset > 0 {
			token = fmt.Sprintf("page-%d", offset/pageSize)
		}
		next := ""
		if end < count {
			next = fmt.Sprintf("page-%d", offset/pageSize+1)
		}
		body, err := json.Marshal(&corev1.PodList{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"},
			ListMeta: metav1.ListMeta{ResourceVersion: "transfer-snapshot", Continue: next},
			Items:    f.pods[offset:end],
		})
		if err != nil {
			tb.Fatal(err)
		}
		f.pages[token] = body
	}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, fmt.Errorf("unexpected method %s", req.Method)
		}
		var body []byte
		if req.URL.Path == "/api/v1/namespaces/team/pods" {
			if req.URL.Query().Get("limit") != fmt.Sprint(podPageSize) || req.URL.Query().Get("labelSelector") != "" || req.URL.Query().Get("fieldSelector") != "" {
				return nil, fmt.Errorf("unexpected Pod list options %s", req.URL.RawQuery)
			}
			f.lists++
			body = f.pages[req.URL.Query().Get("continue")]
		} else {
			f.gets++
			body = responses[req.URL.Path]
		}
		if body == nil {
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	client, err := dynamic.NewForConfigAndClient(&rest.Config{Host: "http://fixture.invalid", QPS: -1}, &http.Client{Transport: transport})
	if err != nil {
		tb.Fatal(err)
	}
	var source dynamic.Interface = client
	if record {
		f.recorder = &podTransferDynamic{Interface: source}
		source = f.recorder
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{podTransferGVK.GroupVersion(), ownerGVK.GroupVersion()})
	mapper.Add(podTransferGVK, meta.RESTScopeNamespace)
	mapper.Add(ownerGVK, meta.RESTScopeNamespace)
	f.reader, err = NewReader(source, mapper)
	if err != nil {
		tb.Fatal(err)
	}
	return f
}

func podTransferPod(name, owner string, ownerGVK schema.GroupVersionKind) corev1.Pod {
	return corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "team", UID: types.UID(name), ResourceVersion: "1",
			Labels: map[string]string{"role": "worker"}, Annotations: map[string]string{"payload": "listed"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: ownerGVK.GroupVersion().String(), Kind: ownerGVK.Kind, Name: owner, UID: types.UID(owner), Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}},
			ManagedFields:   []metav1.ManagedFieldsEntry{{Manager: "fixture", Operation: metav1.ManagedFieldsOperationApply, APIVersion: "v1", FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(`{"f:metadata":{"f:labels":{"f:role":{}}}}`)}},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "worker", Image: "ghcr.io/example/worker:v1", Args: []string{"--fixture"},
				Env: []corev1.EnvVar{
					{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
					{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "example-token"}, Key: "token", Optional: ptr.To(false)}}},
				},
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
			}},
			Volumes: []corev1.Volume{
				{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse("1Gi"))}}},
				{Name: "config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "example-config", DefaultMode: ptr.To(int32(0o400)), Items: []corev1.KeyToPath{{Key: "config", Path: "config"}}}}},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, PodIPs: []corev1.PodIP{{IP: "192.0.2.1"}},
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, Message: "ready"}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "worker", Started: ptr.To(true), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Unix(1, 0).UTC())}}}},
		},
	}
}

func (r *podTransferDynamic) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &podTransferResource{NamespaceableResourceInterface: r.Interface.Resource(gvr), recorder: r}
}

func (r *podTransferResource) Namespace(namespace string) dynamic.ResourceInterface {
	return &podTransferScopedResource{ResourceInterface: r.NamespaceableResourceInterface.Namespace(namespace), recorder: r.recorder}
}

func (r *podTransferScopedResource) List(ctx context.Context, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	page, err := r.ResourceInterface.List(ctx, options)
	if page != nil {
		r.recorder.pages = append(r.recorder.pages, page)
	}
	return page, err
}

func (r *podTransferScopedResource) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	object, err := r.ResourceInterface.Get(ctx, name, options, subresources...)
	if object != nil {
		r.recorder.objects = append(r.recorder.objects, object)
	}
	return object, err
}

func mutatePodTransferJSON(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, element := range value {
			value[key] = mutatePodTransferJSON(element)
		}
		value["transferMutation"] = true
	case []any:
		for i := range value {
			value[i] = mutatePodTransferJSON(value[i])
		}
	case string:
		return value + "-changed"
	case bool:
		return !value
	case int64:
		return value + 1
	case float64:
		return value + 1
	}
	return value
}

func TestListPodsForTransferOwnsNestedPayloads(t *testing.T) {
	f := newPodTransferFixture(t, 4, 2, true)
	transfer, ok := f.reader.(discovery.PodTransferReader)
	if !ok {
		t.Fatal("NewReader() does not implement PodTransferReader")
	}
	first, err := transfer.ListPodsForTransfer(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.reader.ListPods(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.recorder.pages) != 4 || first.ResourceVersion != "transfer-snapshot" || first.Continue != "" {
		t.Fatalf("ListPodsForTransfer() resource version=%q continue=%q raw pages=%d", first.ResourceVersion, first.Continue, len(f.recorder.pages))
	}
	for _, page := range f.recorder.pages {
		for i := range page.Items {
			mutatePodTransferJSON(page.Items[i].Object)
		}
	}
	if !reflect.DeepEqual(first.Items, f.pods) || !reflect.DeepEqual(second.Items, f.pods) {
		t.Fatal("mutating raw dynamic pages changed a returned Pod list")
	}
	pod := &first.Items[0]
	pod.Labels["role"] = "changed"
	pod.Annotations["payload"] = "changed"
	*pod.OwnerReferences[0].Controller = false
	*pod.OwnerReferences[0].BlockOwnerDeletion = false
	pod.OwnerReferences[0].Name = "changed"
	fields := pod.ManagedFields[0].FieldsV1.GetRawBytes()
	fields[0] = 'x'
	pod.ManagedFields[0].FieldsV1.SetRawBytes(fields)
	pod.Spec.Containers[0].Args[0] = "changed"
	pod.Spec.Containers[0].Env[0].ValueFrom.FieldRef.FieldPath = "changed"
	*pod.Spec.Containers[0].Env[1].ValueFrom.SecretKeyRef.Optional = true
	pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")
	pod.Spec.Volumes[0].EmptyDir.SizeLimit.Set(1)
	*pod.Spec.Volumes[1].Secret.DefaultMode = 0o600
	pod.Spec.Volumes[1].Secret.Items[0].Path = "changed"
	pod.Status.PodIPs[0].IP = "192.0.2.2"
	pod.Status.Conditions[0].Message = "changed"
	*pod.Status.ContainerStatuses[0].Started = false
	pod.Status.ContainerStatuses[0].State.Running.StartedAt.Time = time.Unix(2, 0).UTC()
	if !reflect.DeepEqual(first.Items[1:], f.pods[1:]) || !reflect.DeepEqual(second.Items, f.pods) {
		t.Fatal("mutating one transferred Pod changed a sibling or an earlier independent list")
	}
	object, err := f.reader.Get(t.Context(), f.seed)
	if err != nil {
		t.Fatal(err)
	}
	mutatePodTransferJSON(object.Object)
	for _, raw := range f.recorder.objects {
		mutatePodTransferJSON(raw.Object)
	}
	mapping, err := f.reader.Mapping(t.Context(), podTransferGVK)
	if err != nil {
		t.Fatal(err)
	}
	mapping.Resource.Resource = "changed"
	third, err := transfer.ListPodsForTransfer(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second.Items, f.pods) || !reflect.DeepEqual(third.Items, f.pods) {
		t.Fatal("later Get, Mapping, or ListPodsForTransfer calls changed transferred payloads")
	}
	if f.gets != 1 || f.lists != 6 {
		t.Fatalf("adapter calls GET=%d LIST=%d, want 1 and 6", f.gets, f.lists)
	}
}

func TestListPodsForTransferDiscardsIncompleteObservation(t *testing.T) {
	f := newPodTransferFixture(t, 4, 2, false)
	var page corev1.PodList
	if err := json.Unmarshal(f.pages["page-1"], &page); err != nil {
		t.Fatal(err)
	}
	page.ResourceVersion = "changed"
	body, err := json.Marshal(&page)
	if err != nil {
		t.Fatal(err)
	}
	f.pages["page-1"] = body
	got, err := f.reader.(discovery.PodTransferReader).ListPodsForTransfer(t.Context(), "team")
	if got != nil || !errors.Is(err, discovery.ErrIncompleteOwnership) || f.lists != 2 {
		t.Fatalf("ListPodsForTransfer() = %v, %v, LIST=%d, want nil, ErrIncompleteOwnership and 2", got, err, f.lists)
	}
}

func TestReaderWorkloadPodTransfer(t *testing.T) {
	for _, mode := range []string{"transfer", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			const fanout = 2
			f := newPodTransferFixture(t, 4, fanout, false)
			backend := f.reader
			if mode == "fallback" {
				backend = struct{ discovery.Reader }{backend}
			}
			resolver, err := discovery.New(backend, discovery.Options{})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				workload, err := resolver.WorkloadForPod(t.Context(), f.seed)
				if err != nil {
					t.Fatal(err)
				}
				if err := validatePodTransferWorkload(workload, f, fanout); err != nil {
					t.Fatal(err)
				}
			}
			if f.gets != 2*(f.rootCount+1) || f.lists != 2*len(f.pages) {
				t.Fatalf("adapter calls GET=%d LIST=%d, want %d and %d", f.gets, f.lists, 2*(f.rootCount+1), 2*len(f.pages))
			}
		})
	}
}

func validatePodTransferWorkload(workload *discovery.Workload, f *podTransferFixture, fanout int) error {
	if got := workload.Root(); got != f.root {
		return fmt.Errorf("workload root = %+v, want %+v", got, f.root)
	}
	if got := workload.Object(); !reflect.DeepEqual(got, f.rootObject) {
		return fmt.Errorf("workload root payload = %#v, want %#v", got.Object, f.rootObject.Object)
	}
	pods := workload.Pods()
	if len(pods) != fanout {
		return fmt.Errorf("workload Pod count = %d, want %d", len(pods), fanout)
	}
	for i, pod := range pods {
		if !reflect.DeepEqual(pod, &f.pods[i]) {
			return fmt.Errorf("workload Pod %d payload = %+v, want %+v", i, pod, &f.pods[i])
		}
	}
	if issues := workload.MembershipIssues(); len(issues) != 0 {
		return fmt.Errorf("workload membership issues = %+v, want none", issues)
	}
	return nil
}

func BenchmarkReaderWorkloadPodTransfer(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		for _, mode := range []string{"transfer", "fallback"} {
			b.Run(fmt.Sprintf("pods=%d/%s", count, mode), func(b *testing.B) {
				const fanout = 10
				f := newPodTransferFixture(b, count, fanout, false)
				backend := f.reader
				if mode == "fallback" {
					backend = struct{ discovery.Reader }{backend}
				}
				_, transfers := backend.(discovery.PodTransferReader)
				if transfers != (mode == "transfer") {
					b.Fatalf("transfer capability=%t for mode %s", transfers, mode)
				}
				resolver, err := discovery.New(backend, discovery.Options{})
				if err != nil {
					b.Fatal(err)
				}
				var last *discovery.Workload
				b.ReportAllocs()
				for b.Loop() {
					last, err = resolver.WorkloadForPod(b.Context(), f.seed)
					if err != nil {
						b.Fatal(err)
					}
				}
				if err := validatePodTransferWorkload(last, f, fanout); err != nil {
					b.Fatal(err)
				}
				if f.gets != b.N*(f.rootCount+1) || f.lists != b.N*len(f.pages) {
					b.Fatalf("adapter calls GET=%d LIST=%d, want %d and %d", f.gets, f.lists, b.N*(f.rootCount+1), b.N*len(f.pages))
				}
				b.ReportMetric(float64(f.gets)/float64(b.N), "API_GET/op")
				b.ReportMetric(float64(f.lists)/float64(b.N), "API_LIST/op")
				b.ReportMetric(float64(count), "ListedPods/op")
			})
		}
	}
}
