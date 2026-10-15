// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package clientgo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type testMapper struct {
	meta.RESTMapper
	mapping   *meta.RESTMapping
	err       error
	calls     int
	groupKind schema.GroupKind
	versions  []string
	cancel    context.CancelFunc
}

type testScope struct{ name meta.RESTScopeName }
type roundTripFunc func(*http.Request) (*http.Response, error)

var deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

func (m *testMapper) RESTMapping(kind schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	m.calls++
	m.groupKind = kind
	m.versions = versions
	if m.cancel != nil {
		m.cancel()
	}
	return m.mapping, m.err
}
func (s *testScope) Name() meta.RESTScopeName                               { return s.name }
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func mappingFixture() *meta.RESTMapping {
	return &meta.RESTMapping{GroupVersionKind: deploymentGVK, Resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Scope: meta.RESTScopeNamespace}
}

func readerFixture(t *testing.T, mapper meta.RESTMapper, transport roundTripFunc) discovery.Reader {
	t.Helper()
	client, err := dynamic.NewForConfigAndClient(&rest.Config{Host: "http://fixture.invalid"}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(client, mapper)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func jsonResponse(t *testing.T, status int, value any) *http.Response {
	t.Helper()
	if status, ok := value.(metav1.Status); ok {
		status.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}
		value = status
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}

func podFixture(name, namespace string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name)}, Status: corev1.PodStatus{Phase: phase}}
}

func TestNewReaderRequiresDependencies(t *testing.T) {
	var nilClient *dynamic.DynamicClient
	var nilMapper *testMapper
	for _, test := range []struct {
		name   string
		client dynamic.Interface
		mapper meta.RESTMapper
	}{
		{"nil client", nil, &testMapper{}},
		{"typed nil client", nilClient, &testMapper{}},
		{"nil mapper", &dynamic.DynamicClient{}, nil},
		{"typed nil mapper", &dynamic.DynamicClient{}, nilMapper},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewReader(test.client, test.mapper)
			if got != nil || !errors.Is(err, discovery.ErrNoClient) {
				t.Fatalf("NewReader() = %v, %v, want nil and ErrNoClient", got, err)
			}
		})
	}
}

func TestMappingRequiresExactVersionAndScope(t *testing.T) {
	for _, test := range []struct {
		name       string
		change     func(*meta.RESTMapping)
		nilMapping bool
	}{
		{name: "nil mapping", nilMapping: true},
		{name: "nil scope", change: func(m *meta.RESTMapping) { m.Scope = nil }},
		{name: "typed nil scope", change: func(m *meta.RESTMapping) { var scope *testScope; m.Scope = scope }},
		{name: "unknown scope", change: func(m *meta.RESTMapping) { m.Scope = &testScope{name: "other"} }},
		{name: "kind alias", change: func(m *meta.RESTMapping) { m.GroupVersionKind.Kind = "ReplicaSet" }},
		{name: "version alias", change: func(m *meta.RESTMapping) { m.GroupVersionKind.Version = "v1beta1" }},
		{name: "resource version", change: func(m *meta.RESTMapping) { m.Resource.Version = "v1beta1" }},
		{name: "resource group", change: func(m *meta.RESTMapping) { m.Resource.Group = "other" }},
		{name: "empty resource", change: func(m *meta.RESTMapping) { m.Resource.Resource = "" }},
		{name: "subresource", change: func(m *meta.RESTMapping) { m.Resource.Resource = "deployments/status" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := mappingFixture()
			if test.change != nil {
				test.change(m)
			}
			if test.nilMapping {
				m = nil
			}
			mapper := &testMapper{mapping: m}
			r, err := NewReader(&dynamic.DynamicClient{}, mapper)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Mapping(t.Context(), deploymentGVK)
			if got != nil || !errors.Is(err, discovery.ErrInvalidInput) {
				t.Fatalf("Mapping() = %v, %v, want ErrInvalidInput", got, err)
			}
		})
	}
	mapper := &testMapper{mapping: mappingFixture()}
	r, err := NewReader(&dynamic.DynamicClient{}, mapper)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Mapping(t.Context(), deploymentGVK)
	if err != nil {
		t.Fatal(err)
	}
	if mapper.groupKind != deploymentGVK.GroupKind() || !slices.Equal(mapper.versions, []string{"v1"}) {
		t.Fatalf("RESTMapping request = %v %v", mapper.groupKind, mapper.versions)
	}
	got.Resource.Resource = "modified"
	if mapper.mapping.Resource.Resource != "deployments" {
		t.Fatal("Mapping returned shared mutable mapping")
	}
	_, err = r.Mapping(t.Context(), schema.GroupVersionKind{Kind: "Pod"})
	if !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("Mapping without version = %v", err)
	}
}

func TestGetUsesMappedScopeAndVerifiesIdentity(t *testing.T) {
	for _, test := range []struct {
		name      string
		cluster   bool
		actualUID types.UID
		wantErr   error
	}{
		{name: "namespaced", actualUID: "original"},
		{name: "cluster", cluster: true, actualUID: "original"},
		{name: "replacement", actualUID: "replacement", wantErr: ownership.ErrUIDMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			mapper := &testMapper{mapping: mappingFixture()}
			ref := discovery.ObjectRef{GVK: deploymentGVK, Namespace: "team", Name: "workload", UID: "original"}
			path := "/apis/apps/v1/namespaces/team/deployments/workload"
			namespace := "team"
			if test.cluster {
				mapper.mapping.Scope = meta.RESTScopeRoot
				mapper.mapping.GroupVersionKind = schema.GroupVersionKind{Version: "v1", Kind: "Node"}
				mapper.mapping.Resource = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
				ref.GVK = mapper.mapping.GroupVersionKind
				path = "/api/v1/nodes/workload"
				namespace = ""
			}
			calls := 0
			r := readerFixture(t, mapper, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodGet || req.URL.Path != path {
					t.Errorf("request = %s %s, want GET %s", req.Method, req.URL.Path, path)
				}
				return jsonResponse(t, http.StatusOK, map[string]any{"apiVersion": ref.GVK.GroupVersion().String(), "kind": ref.GVK.Kind, "metadata": map[string]any{"name": "workload", "namespace": namespace, "uid": string(test.actualUID)}}), nil
			})
			got, err := r.Get(t.Context(), ref)
			if test.wantErr != nil {
				if got != nil || !errors.Is(err, test.wantErr) {
					t.Fatalf("Get() = %v, %v, want UID mismatch", got, err)
				}
				var identity *ownership.Error
				if !errors.As(err, &identity) || identity.Expected.UID != "original" || identity.Actual.UID != "replacement" {
					t.Fatalf("UID error = %v", err)
				}
			} else if err != nil || got.GetNamespace() != namespace || got.GetUID() != "original" {
				t.Fatalf("Get() = %v, %v", got, err)
			}
			if calls != 1 {
				t.Fatalf("HTTP calls = %d, want 1", calls)
			}
		})
	}
}

func TestGetRejectsInvalidInputAndResponseCoordinates(t *testing.T) {
	for _, test := range []struct {
		name         string
		changeRef    func(*discovery.ObjectRef)
		changeObject func(map[string]any)
		want         error
	}{
		{name: "missing name", changeRef: func(r *discovery.ObjectRef) { r.Name = "" }, want: discovery.ErrInvalidInput},
		{name: "missing UID", changeRef: func(r *discovery.ObjectRef) { r.UID = "" }, want: discovery.ErrInvalidInput},
		{name: "missing namespace", changeRef: func(r *discovery.ObjectRef) { r.Namespace = "" }, want: ownership.ErrScope},
		{name: "wrong name", changeObject: func(o map[string]any) { o["metadata"].(map[string]any)["name"] = "another" }, want: discovery.ErrStaleInput},
		{name: "wrong namespace", changeObject: func(o map[string]any) { o["metadata"].(map[string]any)["namespace"] = "another" }, want: discovery.ErrStaleInput},
		{name: "wrong kind", changeObject: func(o map[string]any) { o["kind"] = "ReplicaSet" }, want: discovery.ErrStaleInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := discovery.ObjectRef{GVK: deploymentGVK, Namespace: "team", Name: "workload", UID: "original"}
			if test.changeRef != nil {
				test.changeRef(&ref)
			}
			calls := 0
			r := readerFixture(t, &testMapper{mapping: mappingFixture()}, func(_ *http.Request) (*http.Response, error) {
				calls++
				o := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "workload", "namespace": "team", "uid": "original"}}
				if test.changeObject != nil {
					test.changeObject(o)
				}
				return jsonResponse(t, http.StatusOK, o), nil
			})
			got, err := r.Get(t.Context(), ref)
			if got != nil || !errors.Is(err, test.want) {
				t.Fatalf("Get() = %v, %v, want %v", got, err, test.want)
			}
			if test.changeRef != nil && calls != 0 {
				t.Fatalf("HTTP calls = %d for invalid input", calls)
			}
		})
	}
}

func TestListPodsExhaustsUnfilteredPages(t *testing.T) {
	for _, namespace := range []string{"team", ""} {
		t.Run("namespace="+namespace, func(t *testing.T) {
			calls := 0
			r := readerFixture(t, &testMapper{}, func(req *http.Request) (*http.Response, error) {
				path := "/api/v1/pods"
				if namespace != "" {
					path = "/api/v1/namespaces/team/pods"
				}
				if req.URL.Path != path {
					t.Errorf("path = %s, want %s", req.URL.Path, path)
				}
				query := req.URL.Query()
				if query.Get("limit") != "500" || query.Get("labelSelector") != "" || query.Get("fieldSelector") != "" || query.Get("resourceVersion") != "" {
					t.Errorf("unexpected query %v", query)
				}
				calls++
				page := &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{ResourceVersion: "snapshot"}}
				switch calls {
				case 1:
					if query.Get("continue") != "" {
						t.Error("first request contains continuation")
					}
					page.Continue = "next"
					page.Items = []corev1.Pod{podFixture("pending", "team", corev1.PodPending)}
				case 2:
					if query.Get("continue") != "next" {
						t.Error("missing continuation")
					}
					other := "team"
					if namespace == "" {
						other = "other"
					}
					pod := podFixture("finished", other, corev1.PodSucceeded)
					now := metav1.Now()
					pod.DeletionTimestamp = &now
					page.Items = []corev1.Pod{pod}
				default:
					t.Fatalf("unexpected request %d", calls)
				}
				return jsonResponse(t, http.StatusOK, page), nil
			})
			got, err := r.ListPods(t.Context(), namespace)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || len(got.Items) != 2 || got.ResourceVersion != "snapshot" || got.Continue != "" || got.RemainingItemCount != nil {
				t.Fatalf("ListPods() = %+v, calls=%d", got, calls)
			}
			if got.Items[0].UID != "pending" || got.Items[1].UID != "finished" || got.Items[1].DeletionTimestamp == nil {
				t.Fatalf("ListPods lost phase/terminating membership: %+v", got.Items)
			}
		})
	}
}

func TestListPodsDiscardsInconsistentPages(t *testing.T) {
	for _, test := range []struct {
		name        string
		secondRV    string
		secondToken string
		status      int
		duplicate   bool
		want        error
		expired     bool
	}{
		{name: "changed resource version", secondRV: "changed", want: discovery.ErrIncompleteOwnership},
		{name: "missing resource version", want: discovery.ErrIncompleteOwnership},
		{name: "repeated continuation", secondRV: "snapshot", secondToken: "next", want: discovery.ErrIncompleteOwnership},
		{name: "duplicate UID", secondRV: "snapshot", duplicate: true, want: discovery.ErrInvalidInput},
		{name: "expired continuation", status: http.StatusGone, expired: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			r := readerFixture(t, &testMapper{}, func(_ *http.Request) (*http.Response, error) {
				calls++
				if calls > 2 {
					t.Fatalf("unexpected continuation retry %d", calls)
				}
				if calls == 2 && test.expired {
					return jsonResponse(t, http.StatusGone, apierrors.NewResourceExpired("expired").ErrStatus), nil
				}
				rv := "snapshot"
				token := "next"
				name := "first"
				if calls == 2 {
					rv = test.secondRV
					token = test.secondToken
					if !test.duplicate {
						name = "second"
					}
				}
				return jsonResponse(t, http.StatusOK, &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{ResourceVersion: rv, Continue: token}, Items: []corev1.Pod{podFixture(name, "team", corev1.PodRunning)}}), nil
			})
			got, err := r.ListPods(t.Context(), "team")
			if got != nil {
				t.Fatalf("ListPods returned partial list: %+v", got)
			}
			if test.expired {
				if !apierrors.IsResourceExpired(err) {
					t.Fatalf("error = %v, want ResourceExpired", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if calls != 2 {
				t.Fatalf("calls = %d, want 2", calls)
			}
		})
	}
}

func TestAPIErrorsRemainInspectable(t *testing.T) {
	for _, test := range []struct {
		name   string
		status *apierrors.StatusError
		match  func(error) bool
	}{
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "workload", errors.New("denied")), apierrors.IsForbidden},
		{"not found", apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "workload"), apierrors.IsNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := readerFixture(t, &testMapper{mapping: mappingFixture()}, func(_ *http.Request) (*http.Response, error) {
				return jsonResponse(t, int(test.status.ErrStatus.Code), test.status.ErrStatus), nil
			})
			ref := discovery.ObjectRef{GVK: deploymentGVK, Namespace: "team", Name: "workload", UID: "original"}
			if object, err := r.Get(t.Context(), ref); object != nil || !test.match(err) {
				t.Fatalf("Get() = %v, %v", object, err)
			}
			if pods, err := r.ListPods(t.Context(), "team"); pods != nil || !test.match(err) {
				t.Fatalf("ListPods() = %v, %v", pods, err)
			}
		})
	}
	noMatch := &meta.NoKindMatchError{GroupKind: deploymentGVK.GroupKind(), SearchedVersions: []string{"v1"}}
	r, err := NewReader(&dynamic.DynamicClient{}, &testMapper{err: noMatch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Mapping(t.Context(), deploymentGVK); !errors.Is(err, noMatch) {
		t.Fatalf("Mapping error = %v, want mapper cause", err)
	}
}

func TestCancellationStopsAcquisition(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	mapper := &testMapper{mapping: mappingFixture()}
	calls := 0
	r := readerFixture(t, mapper, func(_ *http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected HTTP") })
	ref := discovery.ObjectRef{GVK: deploymentGVK, Namespace: "team", Name: "workload", UID: "original"}
	if _, err := r.Mapping(ctx, deploymentGVK); !errors.Is(err, context.Canceled) {
		t.Fatalf("Mapping = %v", err)
	}
	if _, err := r.Get(ctx, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get = %v", err)
	}
	if _, err := r.ListPods(ctx, "team"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPods = %v", err)
	}
	if calls != 0 || mapper.calls != 0 {
		t.Fatalf("cancelled calls: HTTP=%d mapper=%d", calls, mapper.calls)
	}
	ctx, cancel = context.WithCancel(t.Context())
	mapper.cancel = cancel
	if _, err := r.Mapping(ctx, deploymentGVK); !errors.Is(err, context.Canceled) {
		t.Fatalf("Mapping cancelled during lookup = %v", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	calls = 0
	r = readerFixture(t, &testMapper{}, func(_ *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return jsonResponse(t, http.StatusOK, &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{ResourceVersion: "snapshot", Continue: "next"}, Items: []corev1.Pod{podFixture("pod", "team", corev1.PodRunning)}}), nil
	})
	pods, err := r.ListPods(ctx, "team")
	if pods != nil || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled ListPods = %v, %v, calls=%d", pods, err, calls)
	}
}

func TestListPodsRejectsMalformedPage(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any, map[string]any)
		want   error
	}{
		{name: "missing snapshot version", change: func(page, _ map[string]any) { page["metadata"] = map[string]any{"continue": "next"} }, want: discovery.ErrIncompleteOwnership},
		{name: "wrong namespace", change: func(_, pod map[string]any) { pod["metadata"].(map[string]any)["namespace"] = "another" }, want: discovery.ErrInsufficientScope},
		{name: "missing UID", change: func(_, pod map[string]any) { delete(pod["metadata"].(map[string]any), "uid") }, want: discovery.ErrInvalidInput},
		{name: "wrong kind", change: func(_, pod map[string]any) { pod["kind"] = "Namespace" }, want: discovery.ErrInvalidInput},
		{name: "invalid Pod spec", change: func(_, pod map[string]any) { pod["spec"] = "invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			r := readerFixture(t, &testMapper{}, func(_ *http.Request) (*http.Response, error) {
				calls++
				pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "pod", "namespace": "team", "uid": "pod"}}
				page := map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{"resourceVersion": "snapshot"}, "items": []any{pod}}
				test.change(page, pod)
				return jsonResponse(t, http.StatusOK, page), nil
			})
			pods, err := r.ListPods(t.Context(), "team")
			if pods != nil || err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("ListPods() = %v, %v, want nil and %v", pods, err, test.want)
			}
			if calls != 1 {
				t.Fatalf("requests = %d, want 1", calls)
			}
		})
	}
}

func TestGetPropagatesInFlightCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	r := readerFixture(t, &testMapper{mapping: mappingFixture()}, func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	finished := make(chan error, 1)
	go func() {
		_, err := r.Get(ctx, discovery.ObjectRef{GVK: deploymentGVK, Namespace: "team", Name: "workload", UID: "original"})
		finished <- err
	}()
	<-started
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v, want context.Canceled", err)
	}
}
