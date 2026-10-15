// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package clientgo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	adapter "github.com/dsx-ai-factory/workload-map/pkg/discovery/clientgo"
)

type podRESTControlBase struct {
	mappings, gets, metadata, lists int
	onMetadata                      func()
	mapping                         *meta.RESTMapping
	object                          *unstructured.Unstructured
	ownerMetadata                   *metav1.PartialObjectMetadata
	err                             error
	lastContext                     context.Context
	lastGVK                         schema.GroupVersionKind
	lastRef                         discovery.ObjectRef
}

type podRESTPlainBase struct {
	discovery.Reader
}

type podRESTIndexedBase struct {
	discovery.Reader
}

type podRESTIndexedMetadataBase struct {
	discovery.OwnerMetadataReader
}

type podRESTCanceledReader struct{ cancel context.CancelFunc }

type podRESTWrongVersionREST struct{ rest.Interface }

func (b *podRESTControlBase) Mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	b.mappings++
	b.lastContext, b.lastGVK = ctx, gvk
	return b.mapping, b.err
}
func (b *podRESTControlBase) Get(ctx context.Context, ref discovery.ObjectRef) (*unstructured.Unstructured, error) {
	b.gets++
	b.lastContext, b.lastRef = ctx, ref
	return b.object, b.err
}
func (b *podRESTControlBase) GetOwnerMetadata(ctx context.Context, ref discovery.ObjectRef) (*metav1.PartialObjectMetadata, error) {
	b.metadata++
	b.lastContext, b.lastRef = ctx, ref
	if b.onMetadata != nil {
		b.onMetadata()
	}
	return b.ownerMetadata, b.err
}
func (b *podRESTControlBase) ListPods(context.Context, string) (*corev1.PodList, error) {
	b.lists++
	return nil, errors.New("unexpected dynamic-list fallback")
}
func (*podRESTIndexedBase) Capture(context.Context, discovery.ObjectRef) (*discovery.Capture, error) {
	panic("rejected indexed base must not be called")
}
func (*podRESTIndexedMetadataBase) Capture(context.Context, discovery.ObjectRef) (*discovery.Capture, error) {
	panic("rejected indexed metadata base must not be called")
}

type podRESTRoundTripper func(*http.Request) (*http.Response, error)

func (f podRESTRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type podRESTBody struct {
	io.Reader
	onClose func()
}

func (b *podRESTBody) Close() error {
	if b.onClose != nil {
		b.onClose()
	}
	return nil
}

func podRESTControlPod(name string) corev1.Pod {
	controller, block := true, true
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", UID: types.UID(name), Labels: map[string]string{"copy": "original"}, Annotations: map[string]string{"note": "complete"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "shared.example/v1", Kind: "Bundle", Name: "root", UID: "root", Controller: &controller, BlockOwnerDeletion: &block}}, ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "fixture", FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(`{"f:spec":{"f:containers":{}}}`)}}}, Spec: corev1.PodSpec{NodeName: "fixture-node", Containers: []corev1.Container{{Name: "worker", Image: "ghcr.io/example/worker:v1", Args: []string{"--complete"}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

func podRESTControlPage(pods ...corev1.Pod) *corev1.PodList {
	return &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{ResourceVersion: "7"}, Items: pods}
}

func podRESTControlJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func podRESTControlREST(t *testing.T, config *rest.Config, client *http.Client) rest.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	config = rest.CopyConfig(config)
	config.APIPath = "/api"
	config.GroupVersion = &corev1.SchemeGroupVersion
	config.ContentType = "application/json"
	config.NegotiatedSerializer = serializer.NewCodecFactory(scheme).WithoutConversion()
	config.QPS = -1
	clientREST, err := rest.RESTClientForConfigAndClient(config, client)
	if err != nil {
		t.Fatal(err)
	}
	return clientREST
}

func podRESTControlReader(t *testing.T, transport podRESTRoundTripper) (discovery.OwnerMetadataReader, *podRESTControlBase) {
	t.Helper()
	base := &podRESTControlBase{}
	clientREST := podRESTControlREST(t, &rest.Config{Host: "https://fixture.invalid"}, &http.Client{Transport: transport})
	reader, err := adapter.WithPodRESTClient(base, clientREST)
	if err != nil {
		t.Fatal(err)
	}
	metadataReader, ok := reader.(discovery.OwnerMetadataReader)
	if !ok {
		t.Fatal("owner metadata capability was lost")
	}
	return metadataReader, base
}

func podRESTControlResponse(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}
}

func podRESTControlRequest(t *testing.T, request *http.Request, namespace, token string) {
	t.Helper()
	path := "/api/v1/pods"
	if namespace != "" {
		path = "/api/v1/namespaces/" + namespace + "/pods"
	}
	query := request.URL.Query()
	if request.Method != http.MethodGet || request.URL.Path != path || request.Header.Get("Accept") != "application/json" || query.Get("limit") != "500" || query.Get("continue") != token || (len(query) != 1 && token == "") || (len(query) != 2 && token != "") {
		t.Fatalf("unexpected Pod request: %s %s Accept=%q", request.Method, request.URL, request.Header.Get("Accept"))
	}
}

func TestPodRESTReaderDetachmentAndCapabilities(t *testing.T) {
	first := podRESTControlPage(podRESTControlPod("first"), podRESTControlPod("second"))
	first.Continue = "next"
	first.Items[1].Labels = first.Items[0].Labels
	first.Items[1].OwnerReferences = first.Items[0].OwnerReferences
	first.Items[1].Spec.NodeName = ""
	second := podRESTControlPage(podRESTControlPod("third"))
	second.Items[0].Status.Phase = corev1.PodSucceeded
	stamp := metav1.NewTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	second.Items[0].DeletionTimestamp = &stamp
	want := podRESTControlPage()
	for _, page := range []*corev1.PodList{first, second} {
		for _, pod := range page.Items {
			copy := pod.DeepCopy()
			copy.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
			want.Items = append(want.Items, *copy)
		}
	}
	// Compare the complete wire representation, including native Time decoding.
	expectedJSON := podRESTControlJSON(t, want)
	want = &corev1.PodList{}
	if err := json.Unmarshal(expectedJSON, want); err != nil {
		t.Fatal(err)
	}
	bodies := [][]byte{podRESTControlJSON(t, first), podRESTControlJSON(t, second)}
	var borrowed []byte
	calls := 0
	reader, base := podRESTControlReader(t, func(request *http.Request) (*http.Response, error) {
		position := calls % 2
		calls++
		token := ""
		if position == 1 {
			token = "next"
		}
		podRESTControlRequest(t, request, "team", token)
		// The transport reuses its byte storage across pages and invocations.
		borrowed = append(borrowed[:0], bodies[position]...)
		response := podRESTControlResponse(request, http.StatusOK, nil)
		response.Body = &podRESTBody{Reader: bytes.NewReader(borrowed), onClose: func() {
			for i := range borrowed {
				borrowed[i] = 'x'
			}
		}}
		return response, nil
	})
	got, err := reader.ListPods(context.Background(), "team")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("complete detached two-page result differs: %v\ngot=%#v\nwant=%#v", err, got, want)
	}
	base.onMetadata = func() {
		for _, body := range bodies {
			for i := range body {
				body[i] = 'x'
			}
		}
		for i := range borrowed {
			borrowed[i] = 'x'
		}
	}
	reader.Mapping(context.Background(), schema.GroupVersionKind{Version: "v1", Kind: "Pod"})
	reader.Get(context.Background(), discovery.ObjectRef{})
	reader.GetOwnerMetadata(context.Background(), discovery.ObjectRef{})
	if !reflect.DeepEqual(got, want) || base.mappings != 1 || base.gets != 1 || base.metadata != 1 || base.lists != 0 {
		t.Fatal("promoted owner methods changed detached Pods or fell back")
	}
	got.Items[0].Labels["copy"] = "caller mutation"
	*got.Items[0].OwnerReferences[0].Controller = false
	*got.Items[0].OwnerReferences[0].BlockOwnerDeletion = false
	managedFields := got.Items[0].ManagedFields[0].FieldsV1.GetRawBytes()
	managedFields[0] = '['
	got.Items[0].ManagedFields[0].FieldsV1.SetRawBytes(managedFields)
	if got.Items[1].Labels["copy"] != "original" || !*got.Items[1].OwnerReferences[0].Controller || !*got.Items[1].OwnerReferences[0].BlockOwnerDeletion || got.Items[1].ManagedFields[0].FieldsV1.GetRawBytes()[0] != '{' {
		t.Fatal("decoded Pods share mutable nested data")
	}
	bodies = [][]byte{podRESTControlJSON(t, first), podRESTControlJSON(t, second)}
	snapshot := got.DeepCopy()
	again, err := reader.(discovery.PodTransferReader).ListPodsForTransfer(context.Background(), "team")
	if err != nil {
		t.Fatal(err)
	}
	again.Items[0].Spec.Containers[0].Args[0] = "later mutation"
	again.Items[2].Annotations["note"] = "later mutation"
	if !reflect.DeepEqual(got, snapshot) || calls != 4 {
		t.Fatal("repeated results alias earlier data")
	}
}

func TestPodRESTReaderValidation(t *testing.T) {
	mutated := func(change func(*corev1.PodList)) func() []*corev1.PodList {
		return func() []*corev1.PodList {
			p := podRESTControlPage(podRESTControlPod("pod"))
			change(p)
			return []*corev1.PodList{p}
		}
	}
	tests := []struct {
		name  string
		pages func() []*corev1.PodList
		want  error
	}{
		{"wrong list GVK", mutated(func(p *corev1.PodList) { p.Kind = "ServiceList" }), discovery.ErrInvalidInput},
		{"wrong item GVK", mutated(func(p *corev1.PodList) { p.Items[0].TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"} }), discovery.ErrInvalidInput},
		{"missing name", mutated(func(p *corev1.PodList) { p.Items[0].Name = "" }), discovery.ErrInvalidInput},
		{"missing UID", mutated(func(p *corev1.PodList) { p.Items[0].UID = "" }), discovery.ErrInvalidInput},
		{"missing namespace", mutated(func(p *corev1.PodList) { p.Items[0].Namespace = "" }), discovery.ErrInvalidInput},
		{"wrong namespace", mutated(func(p *corev1.PodList) { p.Items[0].Namespace = "elsewhere" }), discovery.ErrInsufficientScope},
		{"duplicate UID", mutated(func(p *corev1.PodList) {
			other := podRESTControlPod("other")
			other.UID = p.Items[0].UID
			p.Items = append(p.Items, other)
		}), discovery.ErrInvalidInput},
		{"duplicate coordinates", mutated(func(p *corev1.PodList) {
			other := podRESTControlPod("pod")
			other.UID = "different"
			p.Items = append(p.Items, other)
		}), discovery.ErrInvalidInput},
		{"zero owner", mutated(func(p *corev1.PodList) { p.Items[0].OwnerReferences = []metav1.OwnerReference{{}} }), discovery.ErrInvalidInput},
		{"incomplete noncontroller", mutated(func(p *corev1.PodList) {
			p.Items[0].OwnerReferences[0].Controller = nil
			p.Items[0].OwnerReferences[0].Name = ""
		}), discovery.ErrInvalidInput},
		{"missing continued RV", mutated(func(p *corev1.PodList) { p.ResourceVersion = ""; p.Continue = "next" }), discovery.ErrIncompleteOwnership},
		{"unreturned items", mutated(func(p *corev1.PodList) { remaining := int64(1); p.RemainingItemCount = &remaining }), discovery.ErrIncompleteOwnership},
		{"changed RV", func() []*corev1.PodList {
			a, b := podRESTControlPage(podRESTControlPod("a")), podRESTControlPage(podRESTControlPod("b"))
			a.Continue, b.ResourceVersion = "next", "8"
			return []*corev1.PodList{a, b}
		}, discovery.ErrIncompleteOwnership},
		{"repeated token", func() []*corev1.PodList {
			a, b := podRESTControlPage(podRESTControlPod("a")), podRESTControlPage(podRESTControlPod("b"))
			a.Continue, b.Continue = "next", "next"
			return []*corev1.PodList{a, b}
		}, discovery.ErrIncompleteOwnership},
		{"duplicate across pages", func() []*corev1.PodList {
			a, b := podRESTControlPage(podRESTControlPod("a")), podRESTControlPage(podRESTControlPod("a"))
			a.Continue = "next"
			return []*corev1.PodList{a, b}
		}, discovery.ErrInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pages, calls := test.pages(), 0
			reader, base := podRESTControlReader(t, func(request *http.Request) (*http.Response, error) {
				if calls == len(pages) {
					t.Fatal("unexpected request after invalid page")
				}
				body := podRESTControlJSON(t, pages[calls])
				calls++
				return podRESTControlResponse(request, http.StatusOK, body), nil
			})
			got, err := reader.(discovery.PodTransferReader).ListPodsForTransfer(context.Background(), "team")
			if got != nil || !errors.Is(err, test.want) || base.lists != 0 {
				t.Fatalf("got=%v err=%v, want %v without fallback", got, err, test.want)
			}
		})
	}
}

func TestPodRESTReaderErrorsCancellationAndEmptyScope(t *testing.T) {
	for _, when := range []string{"before", "after body", "during body"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			reader, base := podRESTControlReader(t, func(request *http.Request) (*http.Response, error) {
				calls++
				response := podRESTControlResponse(request, http.StatusOK, nil)
				if when == "during body" {
					response.Body = &podRESTBody{Reader: podRESTCanceledReader{cancel: cancel}}
				} else {
					response.Body = &podRESTBody{Reader: bytes.NewReader(podRESTControlJSON(t, podRESTControlPage(podRESTControlPod("pod")))), onClose: cancel}
				}
				return response, nil
			})
			if when == "before" {
				cancel()
			}
			got, err := reader.ListPods(ctx, "team")
			if got != nil || !errors.Is(err, context.Canceled) || base.lists != 0 {
				t.Fatalf("cancellation lost: got=%v err=%v", got, err)
			}
			if when == "before" && calls != 0 {
				t.Fatal("pre-cancelled request called transport")
			}
		})
	}
	reader, _ := podRESTControlReader(t, func(request *http.Request) (*http.Response, error) {
		podRESTControlRequest(t, request, "", "")
		a, b := podRESTControlPod("a"), podRESTControlPod("b")
		b.Namespace = "other"
		return podRESTControlResponse(request, http.StatusOK, podRESTControlJSON(t, podRESTControlPage(a, b))), nil
	})
	list, err := reader.ListPods(context.Background(), "")
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("all-namespace list failed: %v", err)
	}
	empty, _ := podRESTControlReader(t, func(request *http.Request) (*http.Response, error) {
		return podRESTControlResponse(request, http.StatusOK, podRESTControlJSON(t, podRESTControlPage())), nil
	})
	list, err = empty.ListPods(context.Background(), "team")
	if err != nil || list == nil || list.Items != nil || list.Continue != "" {
		t.Fatalf("empty shape changed: %#v %v", list, err)
	}
	var nilBase *podRESTControlBase
	var nilPlain *podRESTPlainBase
	var nilIndexed *podRESTIndexedBase
	var nilREST *rest.RESTClient
	clientREST := podRESTControlREST(t, &rest.Config{Host: "https://fixture.invalid"}, &http.Client{})
	for _, dependencies := range []struct {
		base discovery.Reader
		rest rest.Interface
	}{{nil, clientREST}, {nilBase, clientREST}, {nilPlain, clientREST}, {nilIndexed, clientREST}, {&podRESTControlBase{}, nil}, {&podRESTControlBase{}, nilREST}} {
		if _, err := adapter.WithPodRESTClient(dependencies.base, dependencies.rest); !errors.Is(err, discovery.ErrNoClient) {
			t.Fatalf("nil dependency accepted: %v", err)
		}
	}
	if _, err := adapter.WithPodRESTClient(&podRESTControlBase{}, &podRESTWrongVersionREST{Interface: clientREST}); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("non-core REST version accepted: %v", err)
	}
}

func (r podRESTCanceledReader) Read([]byte) (int, error) { r.cancel(); return 0, context.Canceled }

func (*podRESTWrongVersionREST) APIVersion() schema.GroupVersion {
	return schema.GroupVersion{Group: "other", Version: "v1"}
}

func TestPodRESTReaderStatusErrors(t *testing.T) {
	tests := []struct {
		httpCode, statusCode int
		reason               metav1.StatusReason
	}{
		{http.StatusGone, http.StatusGone, metav1.StatusReasonExpired},
		{http.StatusOK, http.StatusGone, metav1.StatusReasonExpired},
		{http.StatusForbidden, http.StatusForbidden, metav1.StatusReasonForbidden},
		{http.StatusNotFound, http.StatusNotFound, metav1.StatusReasonNotFound},
		{http.StatusNotAcceptable, http.StatusNotAcceptable, metav1.StatusReasonNotAcceptable},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.httpCode), func(t *testing.T) {
			status := metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Reason: test.reason, Code: int32(test.statusCode), Message: "fixture Status failure", Details: &metav1.StatusDetails{Name: "pods", Kind: "pods", RetryAfterSeconds: 3, Causes: []metav1.StatusCause{{Type: metav1.CauseTypeFieldValueInvalid, Field: "continue", Message: "fixture cause"}}}}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				podRESTControlRequest(t, request, "team", "")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.httpCode)
				_, _ = w.Write(podRESTControlJSON(t, &status))
			}))
			defer server.Close()
			base := &podRESTControlBase{}
			reader, err := adapter.WithPodRESTClient(base, podRESTControlREST(t, &rest.Config{Host: server.URL}, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			got, err := reader.ListPods(context.Background(), "team")
			var native *apierrors.StatusError
			if got != nil || apierrors.ReasonForError(err) != status.Reason || !errors.As(err, &native) || native.ErrStatus.Message != status.Message || native.ErrStatus.Code != status.Code || !reflect.DeepEqual(native.ErrStatus.Details, status.Details) || calls.Load() != 1 || base.lists != 0 {
				t.Fatalf("Status reason/details or no-fallback lost: %v", err)
			}
		})
	}
}

func TestPodRESTReaderTransportError(t *testing.T) {
	failure := errors.New("fixture transport failure")
	calls := 0
	reader, base := podRESTControlReader(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, failure
	})
	got, err := reader.ListPods(context.Background(), "team")
	if got != nil || !errors.Is(err, failure) || calls != 1 || base.lists != 0 {
		t.Fatalf("transport error identity or no-fallback lost: %v", err)
	}
}

func TestPodRESTReaderMalformedJSON(t *testing.T) {
	valid := string(podRESTControlJSON(t, podRESTControlPage(podRESTControlPod("pod"))))
	tests := []struct {
		name, body string
		valid      bool
	}{
		{"empty", "", false}, {"null", "null", false}, {"array", "[]", false}, {"syntax", "{", false}, {"trailing", valid + "{}", false},
		{"null item", `{"apiVersion":"v1","kind":"PodList","items":[null]}`, false},
		{"wrong items shape", `{"apiVersion":"v1","kind":"PodList","items":{}}`, false},
		{"missing envelope", `{"items":[]}`, false},
		{"missing list version", `{"kind":"PodList","items":[]}`, false},
		{"missing list kind", `{"apiVersion":"v1","items":[]}`, false},
		{"null list version", `{"apiVersion":null,"kind":"PodList","items":[]}`, false},
		{"case mismatched list key", `{"apiVersion":"v1","Kind":"PodList","items":[]}`, false},
		{"duplicate list kind", `{"apiVersion":"v1","kind":"ServiceList","kind":"PodList","items":[]}`, false},
		{"duplicate list metadata", `{"apiVersion":"v1","kind":"PodList","metadata":{"continue":"lost"},"metadata":{"resourceVersion":"7"},"items":[]}`, false},
		{"duplicate item metadata", strings.Replace(valid, `"metadata":{"name":"pod"`, `"metadata":{"name":"other"},"metadata":{"name":"pod"`, 1), false},
		{"duplicate UID", strings.Replace(valid, `"uid":"pod"`, `"uid":"old","uid":"pod"`, 1), false},
		{"duplicate owner controller", strings.Replace(valid, `"controller":true`, `"controller":false,"controller":true`, 1), false},
		{"duplicate map key", strings.Replace(valid, `"copy":"original"`, `"copy":"old","copy":"original"`, 1), false},
		{"unknown extension", strings.Replace(valid, `"kind":"PodList"`, `"kind":"PodList","futureExtension":{"ok":true}`, 1), true},
		{"empty items", `{"apiVersion":"v1","kind":"PodList","items":[]}`, true},
		{"null items", `{"apiVersion":"v1","kind":"PodList","items":null}`, true},
		{"omitted items", `{"apiVersion":"v1","kind":"PodList"}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if strings.HasPrefix(test.name, "duplicate ") && test.body == valid {
				t.Fatal("duplicate fixture did not change")
			}
			calls := 0
			reader, base := podRESTControlReader(t, func(request *http.Request) (*http.Response, error) {
				calls++
				return podRESTControlResponse(request, http.StatusOK, []byte(test.body)), nil
			})
			got, err := reader.ListPods(context.Background(), "team")
			if test.valid {
				if err != nil || got == nil {
					t.Fatalf("valid body rejected: %v", err)
				}
			} else if got != nil || !errors.Is(err, discovery.ErrInvalidInput) {
				t.Fatalf("malformed body published: got=%v err=%v", got, err)
			}
			if calls != 1 || base.lists != 0 {
				t.Fatal("unexpected fallback or retry")
			}
		})
	}
}

func TestPodRESTReaderNativeJSONDecoder(t *testing.T) {
	completeOwner := func() map[string]any {
		return map[string]any{"apiVersion": "shared.example/v1", "kind": "Bundle", "name": "root", "uid": "root", "controller": true, "blockOwnerDeletion": true}
	}
	// These booleans describe adapter.ListPods only. A successful dynamic LIST
	// does not imply that a whole Resolver acquisition accepts malformed owners.
	tests := []struct {
		name                   string
		change                 func(map[string]any, map[string]any)
		rawError, dynamicError bool
		listGVK                string
	}{
		{"missing item GVK", func(map[string]any, map[string]any) {}, false, false, ""},
		{"explicit item GVK", func(p, _ map[string]any) { p["apiVersion"], p["kind"] = "v1", "Pod" }, false, false, ""},
		{"wrong item kind", func(p, _ map[string]any) { p["apiVersion"], p["kind"] = "v1", "Service" }, true, true, ""},
		{"wrong item version", func(p, _ map[string]any) { p["apiVersion"], p["kind"] = "other/v1", "Pod" }, true, true, ""},
		{"partial item GVK", func(p, _ map[string]any) { p["apiVersion"] = "v1" }, true, true, ""},
		{"partial item kind", func(p, _ map[string]any) { p["kind"] = "Pod" }, true, true, ""},
		{"null owner entry", func(_, m map[string]any) { m["ownerReferences"] = []any{nil} }, true, false, ""},
		{"incomplete noncontroller", func(_, m map[string]any) { m["ownerReferences"] = []any{map[string]any{"controller": false}} }, true, false, ""},
		{"complete null controller", func(_, m map[string]any) {
			o := completeOwner()
			o["controller"] = nil
			m["ownerReferences"] = []any{o}
		}, false, false, ""},
		{"complete false controller", func(_, m map[string]any) {
			o := completeOwner()
			o["controller"] = false
			m["ownerReferences"] = []any{o}
		}, false, false, ""},
		{"invalid controller string", func(_, m map[string]any) {
			o := completeOwner()
			o["controller"] = "true"
			m["ownerReferences"] = []any{o}
		}, true, true, ""},
		{"invalid controller number", func(_, m map[string]any) { o := completeOwner(); o["controller"] = 1; m["ownerReferences"] = []any{o} }, true, true, ""},
		{"invalid block value", func(_, m map[string]any) {
			o := completeOwner()
			o["blockOwnerDeletion"] = "false"
			m["ownerReferences"] = []any{o}
		}, true, true, ""},
		{"null owner kind", func(_, m map[string]any) { o := completeOwner(); o["kind"] = nil; m["ownerReferences"] = []any{o} }, true, false, ""},
		{"owner list wrong shape", func(_, m map[string]any) { m["ownerReferences"] = completeOwner() }, true, true, ""},
		{"missing list GVK", func(map[string]any, map[string]any) {}, true, true, "missing"},
		{"wrong list kind", func(map[string]any, map[string]any) {}, true, true, "wrong kind"},
		{"wrong list version", func(map[string]any, map[string]any) {}, true, true, "wrong version"},
		{"wrong list kind explicit Pod item", func(p, _ map[string]any) { p["apiVersion"], p["kind"] = "v1", "Pod" }, true, false, "wrong kind"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			md := map[string]any{"name": "pod", "namespace": "team", "uid": "pod", "ownerReferences": []any{completeOwner()}}
			pod := map[string]any{"metadata": md, "spec": map[string]any{"nodeName": "fixture-node", "containers": []any{map[string]any{"name": "worker", "image": "ghcr.io/example/worker:v1"}}}, "status": map[string]any{"phase": "Running"}}
			test.change(pod, md)
			list := map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{"resourceVersion": "7"}, "items": []any{pod}}
			switch test.listGVK {
			case "missing":
				delete(list, "apiVersion")
				delete(list, "kind")
			case "wrong kind":
				list["kind"] = "ServiceList"
			case "wrong version":
				list["apiVersion"] = "other/v1"
			}
			body, err := json.Marshal(list)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/team/pods" || r.URL.RawQuery != "limit=500" {
					t.Errorf("unexpected native LIST: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer server.Close()
			config := &rest.Config{Host: server.URL, QPS: -1}
			clientREST := podRESTControlREST(t, config, server.Client())
			base := &podRESTControlBase{}
			reader, err := adapter.WithPodRESTClient(base, clientREST)
			if err != nil {
				t.Fatal(err)
			}
			got, rawErr := reader.ListPods(context.Background(), "team")
			if (rawErr != nil) != test.rawError || (test.rawError && got != nil) {
				t.Fatalf("raw result=%v error=%v, want error=%t", got, rawErr, test.rawError)
			}
			d, err := dynamic.NewForConfigAndClient(config, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
			mapper.Add(corev1.SchemeGroupVersion.WithKind("Pod"), meta.RESTScopeNamespace)
			strict, err := adapter.NewReader(d, mapper)
			if err != nil {
				t.Fatal(err)
			}
			dynamicResult, dynamicErr := strict.ListPods(context.Background(), "team")
			if (dynamicErr != nil) != test.dynamicError {
				t.Fatalf("dynamic error=%v, want error=%t", dynamicErr, test.dynamicError)
			}
			if !test.rawError && !test.dynamicError && !reflect.DeepEqual(got, dynamicResult) {
				t.Fatalf("valid native raw/dynamic full Pods differ: raw=%#v dynamic=%#v", got, dynamicResult)
			}
			if requests.Load() != 2 || base.lists != 0 || base.gets != 0 || base.metadata != 0 {
				t.Fatal("unexpected retry or fallback during raw/dynamic decode")
			}
			t.Logf("raw_error=%t dynamic_error=%t", rawErr != nil, dynamicErr != nil)
		})
	}
}

func TestWithPodRESTClientCapabilitiesAndDelegation(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprintf("metadata=%t", metadata), func(t *testing.T) {
			failure := errors.New("delegated failure")
			base := &podRESTControlBase{
				mapping:       &meta.RESTMapping{GroupVersionKind: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}},
				object:        &unstructured.Unstructured{Object: map[string]any{"kind": "Pod"}},
				ownerMetadata: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "owner"}},
				err:           failure,
			}
			var input discovery.Reader = &podRESTPlainBase{Reader: base}
			if metadata {
				input = base
			}
			requests := 0
			clientREST := podRESTControlREST(t, &rest.Config{Host: "https://fixture.invalid"}, &http.Client{
				Transport: podRESTRoundTripper(func(request *http.Request) (*http.Response, error) {
					requests++
					podRESTControlRequest(t, request, "team", "")
					return podRESTControlResponse(request, http.StatusOK, podRESTControlJSON(t, podRESTControlPage())), nil
				}),
			})
			reader, err := adapter.WithPodRESTClient(input, clientREST)
			if err != nil {
				t.Fatal(err)
			}
			if requests != 0 || base.mappings != 0 || base.gets != 0 || base.metadata != 0 || base.lists != 0 {
				t.Fatal("construction invoked a dependency")
			}
			metadataReader, hasMetadata := reader.(discovery.OwnerMetadataReader)
			transfer, hasTransfer := reader.(discovery.PodTransferReader)
			if hasMetadata != metadata || !hasTransfer {
				t.Fatalf("optional capabilities: metadata=%t transfer=%t", hasMetadata, hasTransfer)
			}
			if _, indexed := reader.(discovery.IndexedReader); indexed {
				t.Fatal("direct reader gained an indexed capability")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ref := discovery.ObjectRef{GVK: corev1.SchemeGroupVersion.WithKind("Pod"), Namespace: "team", Name: "pod", UID: "uid"}
			mapping, err := reader.Mapping(ctx, ref.GVK)
			if mapping != base.mapping || err != failure || base.lastContext != ctx || base.lastGVK != ref.GVK {
				t.Fatal("mapping arguments, result, or error changed")
			}
			object, err := reader.Get(ctx, ref)
			if object != base.object || err != failure || base.lastContext != ctx || base.lastRef != ref {
				t.Fatal("object arguments, result, or error changed")
			}
			if hasMetadata {
				object, err := metadataReader.GetOwnerMetadata(ctx, ref)
				if object != base.ownerMetadata || err != failure || base.lastContext != ctx || base.lastRef != ref {
					t.Fatal("metadata arguments, result, or error changed")
				}
			}
			list, err := transfer.ListPodsForTransfer(ctx, "team")
			if err != nil || list == nil || requests != 1 || base.lists != 0 || base.mappings != 1 || base.gets != 1 || (metadata && base.metadata != 1) || (!metadata && base.metadata != 0) {
				t.Fatalf("Pod acquisition used a delegated path: %v", err)
			}
		})
	}
}

func TestWithPodRESTClientRejectsIndexedReaders(t *testing.T) {
	base := &podRESTControlBase{}
	clientREST := podRESTControlREST(t, &rest.Config{Host: "https://fixture.invalid"}, &http.Client{
		Transport: podRESTRoundTripper(func(*http.Request) (*http.Response, error) {
			t.Fatal("constructor called the REST transport")
			return nil, errors.New("unexpected request")
		}),
	})
	for _, input := range []discovery.Reader{
		&podRESTIndexedBase{Reader: base},
		&podRESTIndexedMetadataBase{OwnerMetadataReader: base},
	} {
		if reader, err := adapter.WithPodRESTClient(input, clientREST); reader != nil || !errors.Is(err, discovery.ErrInvalidInput) {
			t.Fatalf("indexed base was accepted: reader=%T err=%v", reader, err)
		}
		if reader, err := adapter.WithPodRESTClient(input, nil); reader != nil || !errors.Is(err, discovery.ErrNoClient) {
			t.Fatalf("nil dependency precedence changed: reader=%T err=%v", reader, err)
		}
	}
	if base.mappings != 0 || base.gets != 0 || base.metadata != 0 || base.lists != 0 {
		t.Fatal("constructor invoked a rejected base")
	}
}
