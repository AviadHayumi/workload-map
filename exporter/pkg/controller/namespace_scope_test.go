// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	ktesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
)

// This is an HTTP scope regression with 10,000 excluded fixtures, not a
// production API-server throughput or memory benchmark.
func TestNamespaceScopeListWatchAndHealth(t *testing.T) {
	const namespace = "selected"
	deploymentGVK := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	definition, err := catalog.Get(deploymentGVK)
	if err != nil {
		t.Fatal(err)
	}
	definition.UID, definition.ResourceVersion = "definition", "1"
	definitionObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(definition)
	if err != nil {
		t.Fatal(err)
	}
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "parent", "namespace": namespace, "uid": "parent", "resourceVersion": "1", "generation": int64(1), "annotations": map[string]any{"submitter": "alice@example.test"}},
		"spec":     map[string]any{"replicas": int64(1), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "worker", "image": "example/worker:v1"}}}}},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Progressing", "status": "True", "reason": "NewReplicaSetAvailable"}}},
	}}
	replicaSet := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{"name": "child", "namespace": namespace, "uid": "child", "resourceVersion": "1", "ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "parent", "uid": "parent", "controller": true}}},
	}}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "worker", "namespace": namespace, "uid": "worker", "resourceVersion": "1", "ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "child", "uid": "child", "controller": true}}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "worker", "image": "example/worker:v1"}}},
		"status":   map[string]any{"phase": "Running"},
	}}
	objects := map[string][]*unstructured.Unstructured{
		"kartas":      {{Object: definitionObject}},
		"deployments": {deployment},
		"replicasets": {replicaSet},
		"pods":        {pod},
	}
	for n := range 10000 {
		objects["deployments"] = append(objects["deployments"], &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": fmt.Sprintf("excluded-%d", n), "namespace": "excluded", "uid": fmt.Sprintf("excluded-%d", n), "resourceVersion": "1"},
		}})
	}
	var mu sync.Mutex
	lists, watches, healthLists := map[string]int{}, map[string]int{}, map[string]int{}
	var unexpected []string
	var excludedTransferred int
	stopServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		var resourceParts []string
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/"):
			resourceParts = parts[2:]
		case strings.HasPrefix(r.URL.Path, "/apis/apps/v1/"), strings.HasPrefix(r.URL.Path, "/apis/run.ai/v1alpha1/"):
			resourceParts = parts[3:]
		}
		requestedNamespace := ""
		if len(resourceParts) >= 3 && resourceParts[0] == "namespaces" {
			requestedNamespace, resourceParts = resourceParts[1], resourceParts[2:]
		}
		if len(resourceParts) == 0 || len(resourceParts) > 2 {
			mu.Lock()
			unexpected = append(unexpected, r.URL.String())
			mu.Unlock()
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		resource := resourceParts[0]
		items, known := objects[resource]
		isWatch := r.URL.Query().Get("watch") == "true"
		isMetadata := strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadata")
		mu.Lock()
		if !known || r.Method != http.MethodGet || (resource == "kartas" && requestedNamespace != "") || (resource != "kartas" && requestedNamespace != namespace) || r.URL.Query().Get("labelSelector") != "" || (resource == "replicasets" && !isMetadata) {
			unexpected = append(unexpected, r.URL.String())
		}
		if isWatch {
			watches[resource]++
		} else if len(resourceParts) == 1 {
			lists[resource]++
			if r.URL.Query().Get("limit") == strconv.Itoa(verificationPageSize) {
				healthLists[resource]++
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var responseItems []any
		for _, item := range items {
			if requestedNamespace != "" && item.GetNamespace() != requestedNamespace {
				continue
			}
			if len(resourceParts) == 2 && item.GetName() != resourceParts[1] {
				continue
			}
			if item.GetNamespace() == "excluded" {
				mu.Lock()
				excludedTransferred++
				mu.Unlock()
			}
			var responseItem any = item
			if isMetadata {
				responseItem = &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"}, ObjectMeta: metav1.ObjectMeta{Name: item.GetName(), Namespace: item.GetNamespace(), UID: item.GetUID(), ResourceVersion: item.GetResourceVersion(), Generation: item.GetGeneration(), Labels: item.GetLabels(), Annotations: item.GetAnnotations(), OwnerReferences: item.GetOwnerReferences()}}
			}
			responseItems = append(responseItems, responseItem)
		}
		if isWatch {
			w.WriteHeader(http.StatusOK)
			if r.URL.Query().Get("sendInitialEvents") == "true" {
				for _, item := range responseItems {
					_ = json.NewEncoder(w).Encode(map[string]any{"type": "ADDED", "object": item})
				}
				apiVersion, kind := items[0].GetAPIVersion(), items[0].GetKind()
				if isMetadata {
					apiVersion, kind = "meta.k8s.io/v1", "PartialObjectMetadata"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "BOOKMARK", "object": map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"resourceVersion": "1", "annotations": map[string]any{"k8s.io/initial-events-end": "true"}}}})
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-stopServer:
			}
			return
		}
		if len(resourceParts) == 2 {
			if len(responseItems) != 1 {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(responseItems[0])
			return
		}
		apiVersion, kind := "v1", "List"
		if len(items) > 0 {
			apiVersion, kind = items[0].GetAPIVersion(), items[0].GetKind()+"List"
		}
		if isMetadata {
			apiVersion, kind = "meta.k8s.io/v1", "PartialObjectMetadataList"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"resourceVersion": "1"}, "items": responseItems})
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(stopServer) })
	config := &rest.Config{Host: server.URL, QPS: 100, Burst: 100}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	metadataClient, err := metadata.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	discovery := &discoveryfake.FakeDiscovery{Fake: &ktesting.Fake{}}
	discovery.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true}, {Name: "replicasets", Kind: "ReplicaSet", Namespaced: true}}},
		{GroupVersion: "run.ai/v1alpha1", APIResources: []metav1.APIResource{{Name: "kartas", Kind: "Karta"}}},
	}
	evaluator, err := identity.New(identity.Config{SubmittedByExpression: "'submitter' in annotations ? annotations['submitter'] : ''"})
	if err != nil {
		t.Fatal(err)
	}
	s := store.New()
	controller, err := New(dynamicClient, metadataClient, kubeClient, restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discovery)), registry.New(), owner.New(), s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Namespace: namespace, Identity: evaluator, HealthInterval: 50 * time.Millisecond, HealthMaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = controller.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if runErr != nil {
				t.Errorf("controller stopped: %v", runErr)
			}
		case <-time.After(5 * time.Second):
			t.Error("controller did not stop")
		}
	})
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := s.Snapshot()
		healthy := false
		for _, h := range snapshot.Health {
			if h.Scope == "workload" && h.UID == "parent" && h.Value(time.Now()) == 1 {
				healthy = true
			}
		}
		mu.Lock()
		complete := len(unexpected) == 0 && excludedTransferred == 0
		for _, resource := range []string{"pods", "deployments", "replicasets", "kartas"} {
			complete = complete && lists[resource] > 0 && watches[resource] > 0 && healthLists[resource] > 0
		}
		requestFailure := len(unexpected) != 0 || excludedTransferred != 0
		requestDetail := fmt.Sprintf("lists=%v watches=%v health=%v unexpected=%v excludedTransferred=%d", lists, watches, healthLists, unexpected, excludedTransferred)
		mu.Unlock()
		if requestFailure {
			t.Fatal(requestDetail)
		}
		if complete && controller.Ready() && healthy && len(snapshot.Pods) == 1 && len(snapshot.Workloads) == 1 && snapshot.Pods[0].WorkloadUID == "parent" && snapshot.Pods[0].OwnerResolution == "resolved" {
			if snapshot.Identities["parent"].Result.Submitter != "alice@example.test" || snapshot.Identities["worker"].Result.Submitter != "" {
				t.Fatalf("root-only identity lost or invented on child: %+v", snapshot.Identities)
			}
			t.Logf("10,000 excluded workload fixtures transferred zero objects; %s", requestDetail)
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("scope/attribution did not converge: %s; snapshot=%+v", requestDetail, snapshot)
		case <-ticker.C:
		}
	}
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	registration, err := collector.New(provider.Meter("namespace-scope-test"), s, collector.Options{Identity: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registration.Unregister() })
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != collector.MetricPodWorkloadInfo && metric.Name != collector.MetricWorkloadStatus {
				continue
			}
			for _, point := range metric.Data.(metricdata.Gauge[float64]).DataPoints {
				submitter, _ := point.Attributes.Value(attribute.Key(collector.LabelSubmittedBy))
				workload, _ := point.Attributes.Value(attribute.Key(collector.LabelWorkloadUID))
				if submitter.AsString() != "alice@example.test" || workload.AsString() != "parent" {
					t.Fatalf("%s lost parent identity: %v", metric.Name, point.Attributes)
				}
				seen[metric.Name]++
			}
		}
	}
	if seen[collector.MetricPodWorkloadInfo] != 1 || seen[collector.MetricWorkloadStatus] == 0 {
		t.Fatalf("missing attributed metrics: %v", seen)
	}
}
