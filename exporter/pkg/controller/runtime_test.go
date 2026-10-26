// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/submitterextractor"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

type connectedFixture struct {
	controller *Runtime
	client     *fake.FakeDynamicClient
	store      *store.Store
	now        atomic.Int64
}

type connectedHooks struct {
	WrapDynamic   func(dynamic.Interface) dynamic.Interface
	WrapInventory func(kartadefinitioninventory.Interface) kartadefinitioninventory.Interface
	WrapSubmitter func(submitterextractor.Interface) submitterextractor.Interface
	BeforeRun     func(*Runtime)
}

func startConnected(t *testing.T, objects []*unstructured.Unstructured, configure func(*RuntimeOptions), expression string, wrap ...func(dynamic.Interface) dynamic.Interface) *connectedFixture {
	t.Helper()
	return startConnectedWithHooks(t, objects, configure, expression, connectedHooks{
		WrapDynamic: func(client dynamic.Interface) dynamic.Interface {
			for _, transform := range wrap {
				client = transform(client)
			}
			return client
		},
	})
}

func startConnectedWithHooks(t *testing.T, objects []*unstructured.Unstructured, configure func(*RuntimeOptions), expression string, hooks connectedHooks) *connectedFixture {
	t.Helper()
	namespace := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{
			"name": "team", "uid": "namespace", "resourceVersion": "1", "labels": map[string]any{"kubernetes.io/metadata.name": "team"},
		},
	}}
	objects = append(objects, namespace)
	resources := make([]runtime.Object, 0, len(objects))
	for _, object := range objects {
		resources = append(resources, object)
	}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "namespaces"}:                      "NamespaceList",
		{Version: "v1", Resource: "pods"}:                            "PodList",
		{Group: "batch", Version: "v1", Resource: "jobs"}:            "JobList",
		{Group: "batch", Version: "v2", Resource: "jobs"}:            "JobList",
		{Group: "run.ai", Version: "v1alpha1", Resource: "kartas"}:   "KartaList",
		{Group: "example.com", Version: "v1", Resource: "trainings"}: "TrainingList",
	}, resources...)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}, {Group: "batch", Version: "v1"}, {Group: "example.com", Version: "v1"}})
	mapper.Add(runtimePodGVK, meta.RESTScopeNamespace)
	mapper.Add(namespaceGVK, meta.RESTScopeRoot)
	mapper.Add(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "batch", Version: "v2", Kind: "Job"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"}, meta.RESTScopeNamespace)
	options := RuntimeOptions{Definitions: kartadefinitioninventory.DefaultOptions(), RefreshInterval: time.Hour, MaxAge: 2 * time.Hour}
	options.Definitions.Cluster.Enabled = false
	options.Definitions.Catalog.Enabled = true
	options.Definitions.Catalog.Include = []string{"batch-job-v1"}
	if configure != nil {
		configure(&options)
	}
	fixture := &connectedFixture{client: client, store: store.New()}
	var runtimeClient dynamic.Interface = client
	if hooks.WrapDynamic != nil {
		runtimeClient = hooks.WrapDynamic(runtimeClient)
	}
	fixture.now.Store(time.Now().UnixNano())
	options.Clock = func() time.Time { return time.Unix(0, fixture.now.Load()) }
	inventory, err := kartadefinitioninventory.New(runtimeClient, options.Definitions)
	if err != nil {
		t.Fatal(err)
	}
	var extractor submitterextractor.Interface
	if expression != "" {
		extractor, err = submitterextractor.New(expression)
		if err != nil {
			t.Fatal(err)
		}
	}
	var selected kartadefinitioninventory.Interface = inventory
	if hooks.WrapInventory != nil {
		selected = hooks.WrapInventory(selected)
	}
	if hooks.WrapSubmitter != nil {
		extractor = hooks.WrapSubmitter(extractor)
	}
	reader := NewInformerReader()
	d := discovery.New(reader, selected)
	fixture.controller, err = NewRuntime(runtimeClient, nil, mapper, reader, d, selected, extractor, fixture.store, options)
	if err != nil {
		t.Fatal(err)
	}
	if hooks.BeforeRun != nil {
		hooks.BeforeRun(fixture.controller)
	}
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 2)
	go func() { results <- inventory.Run(ctx) }()
	go func() { results <- fixture.controller.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		for range 2 {
			select {
			case err := <-results:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("controller did not shut down")
			}
		}
	})
	return fixture
}

func runtimeObject(gvk schema.GroupVersionKind, name string, uid types.UID) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	object.SetNamespace("team")
	object.SetName(name)
	object.SetUID(uid)
	object.SetResourceVersion("1")
	object.SetCreationTimestamp(metav1.NewTime(time.Unix(100, 0)))
	return object
}

func runtimeJob(name string, uid types.UID) *unstructured.Unstructured {
	job := runtimeObject(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}, name, uid)
	job.Object["spec"] = map[string]any{"parallelism": int64(1), "completions": int64(1), "template": map[string]any{
		"spec": map[string]any{"containers": []any{map[string]any{"name": "work", "image": "example.com/work:v1"}}},
	}}
	job.Object["status"] = map[string]any{"active": int64(1)}
	return job
}

func runtimePod(name string, uid types.UID, owner *unstructured.Unstructured) *unstructured.Unstructured {
	pod := runtimeObject(runtimePodGVK, name, uid)
	pod.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(), Name: owner.GetName(), UID: owner.GetUID(), Controller: ptr.To(true),
	}})
	pod.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "work", "image": "example.com/work:v1"}}, "nodeName": "example-node"}
	pod.Object["status"] = map[string]any{"phase": "Running"}
	return pod
}

func awaitRuntime(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connected runtime condition did not converge")
}

func TestRuntimeHiddenPodsParticipateInSubmitterAgreement(t *testing.T) {
	job := runtimeJob("work", "root")
	job.SetAnnotations(map[string]string{"submitter": "alice"})
	visible := runtimePod("visible", "visible", job)
	visible.SetLabels(map[string]string{"visible": "true"})
	visible.SetAnnotations(map[string]string{"submitter": "alice"})
	hidden := runtimePod("hidden", "hidden", job)
	hidden.SetAnnotations(map[string]string{"submitter": "bob"})
	fixture := startConnected(t, []*unstructured.Unstructured{job, visible, hidden}, func(options *RuntimeOptions) {
		options.PodSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"visible": "true"}}
	}, `"submitter" in annotations ? annotations["submitter"] : ""`)
	awaitRuntime(t, func() bool {
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		record := view.Records[0]
		row, exists := record.Pod("visible")
		valid, _ := view.Valid(row.Mapping)
		_, hiddenEmitted := record.Pod("hidden")
		return exists && valid && !hiddenEmitted && record.Workload.HasStatus && record.SubmittedBy == ""
	})
	if err := fixture.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("team").
		Delete(context.Background(), "hidden", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		record := view.Records[0]
		valid, _ := view.Valid(record.Membership)
		return valid && record.SubmittedBy == "alice"
	})
}

func TestRuntimeZeroPodRootAndCapturedExpiry(t *testing.T) {
	fixture := startConnected(t, []*unstructured.Unstructured{runtimeJob("zero", "root")}, nil, "")
	awaitRuntime(t, func() bool {
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		record := view.Records[0]
		status, _ := view.Valid(record.Status)
		creation, _ := view.Valid(record.Creation)
		return record.Workload.HasStatus && status && creation
	})
	before := fixture.store.Capture()
	fixture.now.Add(int64(3 * time.Hour))
	expired := fixture.store.Capture()
	if valid, _ := expired.Valid(expired.Records[0].Status); valid {
		t.Fatal("status did not expire")
	}
	if valid, _ := expired.Valid(expired.Records[0].Creation); valid {
		t.Fatal("creation did not expire independently")
	}
	if valid, _ := before.Valid(before.Records[0].Status); !valid {
		t.Fatal("retained capture changed after clock advance")
	}
}

func TestRuntimeGenericRootWithoutKarta(t *testing.T) {
	root := runtimeObject(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"}, "generic", "generic")
	pod := runtimePod("worker", "pod", root)
	fixture := startConnected(t, []*unstructured.Unstructured{root, pod}, func(options *RuntimeOptions) {
		options.Definitions.Catalog.Enabled = false
	}, "")
	awaitRuntime(t, func() bool {
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		record := view.Records[0]
		row, exists := record.Pod("pod")
		valid, _ := view.Valid(row.Mapping)
		return exists && valid && row.Record.Component == "<unknown>" && row.Record.Instance == "<unknown>" && !record.Workload.HasStatus
	})
}

func TestRuntimeDefinitionCandidateAcceptanceRevokesOnlyChangedWinner(t *testing.T) {
	var definition *unstructured.Unstructured
	for _, candidate := range catalog.List() {
		if candidate.Name == "batch-job-v1" {
			body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(candidate)
			if err != nil {
				t.Fatal(err)
			}
			definition = &unstructured.Unstructured{Object: body}
			break
		}
	}
	if definition == nil {
		t.Fatal("Job catalog definition is missing")
	}
	definition.SetAPIVersion("run.ai/v1alpha1")
	definition.SetKind("Karta")
	definition.SetName("b-selected")
	definition.SetUID("selected")
	definition.SetResourceVersion("1")
	loser := definition.DeepCopy()
	loser.SetName("z-loser")
	loser.SetUID("loser")
	fixture := startConnected(t, []*unstructured.Unstructured{runtimeJob("work", "root"), definition, loser}, func(options *RuntimeOptions) {
		options.Definitions.Cluster.Enabled = true
	}, "")
	awaitRuntime(t, func() bool {
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		record := view.Records[0]
		valid, _ := view.Valid(record.Status)
		return record.Workload.Karta == "b-selected" && record.Workload.HasStatus && valid
	})
	loser.SetResourceVersion("2")
	loser.SetAnnotations(map[string]string{"changed": "yes"})
	fixture.controller.acceptDefinition(loser, false)
	view := fixture.store.Capture()
	if len(view.Records) != 1 || !view.Records[0].Workload.HasStatus {
		t.Fatal("losing-candidate acceptance revoked the selected interpretation")
	}
	winner := definition.DeepCopy()
	winner.SetName("a-replacement")
	winner.SetUID("replacement")
	fixture.controller.acceptDefinition(winner, false)
	view = fixture.store.Capture()
	if len(view.Records) != 1 || view.Records[0].Workload.HasStatus || len(view.Records[0].PodPages) != 0 {
		t.Fatal("new winner was not revoked before the event callback returned")
	}
}
