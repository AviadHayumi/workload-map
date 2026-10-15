// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package clientgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

const ownerMetadataAccept = "application/vnd.kubernetes.protobuf;as=PartialObjectMetadata;g=meta.k8s.io;v=v1,application/json;as=PartialObjectMetadata;g=meta.k8s.io;v=v1,application/json"

func ownerMetadataHTTPReader(t *testing.T, handler http.HandlerFunc, mapper meta.RESTMapper) discovery.OwnerMetadataReader {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config := &rest.Config{Host: server.URL, QPS: -1}
	d, err := dynamic.NewForConfigAndClient(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	m, err := metadata.NewForConfigAndClient(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReaderWithOwnerMetadata(d, m, mapper)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOwnerMetadataHTTPNegotiation(t *testing.T) {
	for _, format := range []string{"json", "protobuf", "full JSON", "unknown GVK JSON"} {
		for _, cluster := range []bool{false, true} {
			t.Run(format+"/cluster="+map[bool]string{false: "false", true: "true"}[cluster], func(t *testing.T) {
				object := ownerMetadataObject()
				ref := ownerMetadataRef()
				mapping := mappingFixture()
				path := "/apis/apps/v1/namespaces/team/deployments/workload"
				if cluster {
					mapping.Scope = meta.RESTScopeRoot
					mapping.GroupVersionKind = schema.GroupVersionKind{Version: "v1", Kind: "Node"}
					mapping.Resource = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
					ref.GVK = mapping.GroupVersionKind
					object.Namespace = ""
					path = "/api/v1/nodes/workload"
				}
				var body []byte
				var err error
				contentType := "application/json"
				expected := object.DeepCopy()
				// Stock metadata decoding clears the envelope even for negotiated POM.
				expected.TypeMeta = metav1.TypeMeta{}
				expected.Labels = nil
				expected.Annotations = nil
				expected.Finalizers = nil
				switch format {
				case "protobuf":
					var encoded bytes.Buffer
					err = protobuf.NewSerializer(nil, nil).Encode(object, &encoded)
					body = encoded.Bytes()
					contentType = runtime.ContentTypeProtobuf
				case "full JSON", "unknown GVK JSON":
					wire := map[string]any{"apiVersion": ref.GVK.GroupVersion().String(), "kind": ref.GVK.Kind, "metadata": object.ObjectMeta, "spec": map[string]any{"nested": []any{map[string]any{"payload": "complete"}}}, "status": map[string]any{"ready": true}}
					if format == "unknown GVK JSON" {
						wire["apiVersion"] = "unrelated.example/v9"
						wire["kind"] = "WrongOriginalKind"
					}
					body, err = json.Marshal(wire)
					expected.TypeMeta = metav1.TypeMeta{}
				default:
					body, err = json.Marshal(object)
				}
				if err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				r := ownerMetadataHTTPReader(t, func(w http.ResponseWriter, req *http.Request) {
					calls.Add(1)
					if req.Method != http.MethodGet || req.URL.Path != path || req.URL.RawQuery != "" {
						t.Errorf("request = %s %s", req.Method, req.URL)
					}
					if calls.Load() == 1 && req.Header.Get("Accept") != ownerMetadataAccept {
						t.Errorf("Accept = %q", req.Header.Get("Accept"))
					}
					w.Header().Set("Content-Type", contentType)
					_, _ = w.Write(body)
				}, &testMapper{mapping: mapping})
				got, err := r.GetOwnerMetadata(t.Context(), ref)
				if err != nil || !reflect.DeepEqual(got, expected) || calls.Load() != 1 {
					t.Fatalf("metadata = %#v, %v; requests=%d, expected %#v", got, err, calls.Load(), expected)
				}
				if format == "unknown GVK JSON" {
					// Stock fallback erased the wire GVK; full Get still checks it.
					full, err := r.Get(t.Context(), ref)
					if full != nil || !errors.Is(err, discovery.ErrStaleInput) || calls.Load() != 2 {
						t.Fatalf("strict Get = %v, %v; requests=%d", full, err, calls.Load())
					}
				}
			})
		}
	}
}

func TestOwnerMetadataHTTPFailuresDoNotFallback(t *testing.T) {
	for _, tt := range []struct {
		name   string
		code   int
		body   string
		reason metav1.StatusReason
	}{
		{name: "forbidden", code: 403, reason: metav1.StatusReasonForbidden},
		{name: "not found", code: 404, reason: metav1.StatusReasonNotFound},
		{name: "not acceptable", code: 406, reason: metav1.StatusReasonNotAcceptable},
		{name: "broken JSON", code: 200, body: `{"apiVersion":`},
		{name: "wrong registered type", code: 200, body: `{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadataList","items":[]}`},
		{name: "invalid owner shape", code: 200, body: `{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadata","metadata":{"name":"workload","namespace":"team","uid":"original","ownerReferences":{}}}`},
		{name: "invalid owner scalar", code: 200, body: `{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadata","metadata":{"name":"workload","namespace":"team","uid":"original","ownerReferences":[{"uid":42}]}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			if tt.reason != "" {
				var err error
				body, err = json.Marshal(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Code: int32(tt.code), Reason: tt.reason})
				if err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			r := ownerMetadataHTTPReader(t, func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				if req.Header.Get("Accept") != ownerMetadataAccept {
					t.Error("unexpected dynamic fallback request")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.code)
				_, _ = w.Write(body)
			}, &testMapper{mapping: mappingFixture()})
			got, err := r.GetOwnerMetadata(t.Context(), ownerMetadataRef())
			if got != nil || err == nil || calls.Load() != 1 {
				t.Fatalf("metadata = %v, %v; requests=%d", got, err, calls.Load())
			}
			if tt.reason != "" {
				var status *apierrors.StatusError
				if !errors.As(err, &status) || status.ErrStatus.Reason != tt.reason || int(status.ErrStatus.Code) != tt.code {
					t.Fatalf("status error = %v", err)
				}
			}
		})
	}
}

func TestOwnerMetadataHTTPRetainsMalformedEntriesForSDKValidation(t *testing.T) {
	for _, owners := range []string{"null", "[]", "[null]", "[{}]", `[{"apiVersion":"v1","kind":"ConfigMap","name":"valid","uid":"valid"},null]`} {
		t.Run(owners, func(t *testing.T) {
			r := ownerMetadataHTTPReader(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadata","metadata":{"name":"workload","namespace":"team","uid":"original","ownerReferences":` + owners + `}}`))
			}, &testMapper{mapping: mappingFixture()})
			got, err := r.GetOwnerMetadata(t.Context(), ownerMetadataRef())
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if strings.HasPrefix(owners, "[{") || owners == "[null]" {
				want = 1
			}
			if strings.Contains(owners, ",null") {
				want = 2
			}
			if len(got.OwnerReferences) != want {
				t.Fatalf("lost decoded entries: %+v", got.OwnerReferences)
			}
			if want > 0 && got.OwnerReferences[want-1] != (metav1.OwnerReference{}) {
				t.Fatalf("malformed entry normalized: %+v", got.OwnerReferences)
			}
		})
	}
}

func TestOwnerMetadataHTTPCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	r := ownerMetadataHTTPReader(t, func(_ http.ResponseWriter, req *http.Request) { calls.Add(1); cancel(); <-req.Context().Done() }, &testMapper{mapping: mappingFixture()})
	got, err := r.GetOwnerMetadata(ctx, ownerMetadataRef())
	if got != nil || !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("metadata = %v, %v; requests=%d", got, err, calls.Load())
	}
}
