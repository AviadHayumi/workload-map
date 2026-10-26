// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
)

func TestRuntimeSelectedVersionHandoverRejectsOldCallbacks(t *testing.T) {
	root := runtimeJob("work", "root")
	alias := root.DeepCopy()
	alias.SetAPIVersion("batch/v2")
	pod := runtimePod("worker", "pod", root)
	fixture := startConnected(t, []*unstructured.Unstructured{root, alias, pod}, func(options *RuntimeOptions) {
		options.Definitions.Cluster.Enabled = true
	}, "")
	awaitRuntime(t, func() bool {
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		row, exists := view.Records[0].Pod("pod")
		valid, _ := view.Valid(row.Mapping)
		return exists && valid && row.Record.Workload.Version == "v1"
	})
	fixture.controller.mu.Lock()
	var old *resourceStream
	for _, stream := range fixture.controller.reader.streams {
		if stream.gvk == root.GroupVersionKind() {
			old = stream
		}
	}
	fixture.controller.mu.Unlock()
	if old == nil || !old.full {
		t.Fatal("initial full-object generation missing")
	}
	definition := kartas.BatchJob()
	definition.Name, definition.UID, definition.ResourceVersion = "version-two", "definition", "1"
	definition.Spec.StructureDefinition.RootComponent.Kind.Version = "v2"
	body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(definition)
	if err != nil {
		t.Fatal(err)
	}
	karta := &unstructured.Unstructured{Object: body}
	karta.SetAPIVersion("run.ai/v1alpha1")
	karta.SetKind("Karta")
	if _, err := fixture.client.Resource(kartaGVR).Create(context.Background(), karta, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		record := view.Records[0]
		row, exists := record.Pod("pod")
		valid, _ := view.Valid(row.Mapping)
		return exists && valid && record.Workload.Ref.Version == "v2" && row.Record.Workload.Version == "v2"
	})
	fixture.controller.mu.Lock()
	replacement := fixture.controller.reader.streams[old.key]
	if replacement == old || replacement.full || !replacement.ready || !replacement.handler.HasSynced() {
		fixture.controller.mu.Unlock()
		t.Fatal("old representation did not complete metadata generation handover")
	}
	fixture.controller.mu.Unlock()
	stale := root.DeepCopy()
	stale.SetResourceVersion("99")
	fixture.controller.acceptObject(old, stale, false)
	fixture.controller.acceptObject(old, root, true)
	view := fixture.store.Capture()
	if len(view.Records) != 1 || view.Records[0].Workload.Ref.Version != "v2" {
		t.Fatal("old callback changed the selected generation")
	}
	if row, exists := view.Records[0].Pod("pod"); !exists || row.Record.Workload.Version != "v2" {
		t.Fatal("old callback removed current Pod mapping")
	}
	namespace, err := fixture.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).
		Get(context.Background(), "team", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fixture.controller.acceptNamespace(namespace, true)
	fixture.controller.mu.Lock()
	defer fixture.controller.mu.Unlock()
	if len(fixture.controller.retired) != 0 || len(fixture.controller.rootsByNamespace) != 0 ||
		len(fixture.controller.podsByRoot) != 0 || len(fixture.controller.associations) != 0 {
		t.Fatal("namespace removal retained obsolete lifetime indexes")
	}
}

func TestRuntimeOneUnresolvedChildPreservesIndependentFields(t *testing.T) {
	root := runtimeJob("work", "root")
	root.SetAnnotations(map[string]string{"submitter": "alice"})
	good := runtimePod("good", "good", root)
	bad := runtimePod("bad", "bad", root)
	fixture := startConnected(t, []*unstructured.Unstructured{root, good, bad}, nil,
		`"submitter" in annotations ? annotations["submitter"] : ""`)
	awaitRuntime(t, func() bool {
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		record := view.Records[0]
		_, goodPresent := record.Pod("good")
		_, badPresent := record.Pod("bad")
		complete, _ := view.Valid(record.Membership)
		return goodPresent && badPresent && complete && record.SubmittedBy == "alice"
	})
	bad.SetResourceVersion("2")
	bad.SetOwnerReferences(append(bad.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion: "batch/v1", Kind: "Job", Name: "missing", UID: "missing", Controller: ptr.To(true),
	}))
	if _, err := fixture.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).
		Namespace("team").Update(context.Background(), bad, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		fixture.controller.mu.Lock()
		accepted := fixture.controller.reader.objects[refKey(objectReference(bad, runtimePodGVK))].observation.Ref.ResourceVersion == "2"
		fixture.controller.mu.Unlock()
		view := fixture.store.Capture()
		if !accepted || len(view.Records) != 1 {
			return false
		}
		record := view.Records[0]
		statusValid, _ := view.Valid(record.Status)
		creationValid, _ := view.Valid(record.Creation)
		row, goodPresent := record.Pod("good")
		mappingValid, _ := view.Valid(row.Mapping)
		_, badPresent := record.Pod("bad")
		membershipValid, _ := view.Valid(record.Membership)
		return record.Workload.HasStatus && statusValid && creationValid && goodPresent && mappingValid &&
			!badPresent && !membershipValid && record.SubmittedBy == ""
	})
}

func TestRuntimeInvalidDefinitionDoesNotPoisonPeerMembership(t *testing.T) {
	good := runtimeJob("good", "good")
	good.SetAnnotations(map[string]string{"submitter": "alice"})
	bad := runtimeJob("bad", "bad")
	bad.SetGroupVersionKind(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"})
	fixture := startConnected(t, []*unstructured.Unstructured{
		good, bad, runtimePod("good-pod", "good-pod", good), runtimePod("bad-pod", "bad-pod", bad),
		invariantKarta("broken", "(", bad.GroupVersionKind()),
	}, func(options *RuntimeOptions) {
		options.Definitions.Cluster.Enabled = true
	}, `"submitter" in annotations ? annotations["submitter"] : ""`)
	awaitRuntime(t, func() bool {
		view, healthy := invariantPublication(fixture, good.GetUID())
		_, failed := invariantPublication(fixture, bad.GetUID())
		if healthy == nil || failed == nil || !slices.Contains(failed.FailureReasons, store.ReasonInvalidDefinition) {
			return false
		}
		status, _ := view.Valid(healthy.Status)
		membership, _ := view.Valid(healthy.Membership)
		row, present := healthy.Pod("good-pod")
		mapping, _ := view.Valid(row.Mapping)
		return status && membership && present && mapping && healthy.SubmittedBy == "alice"
	})
}

func TestRuntimeGenericRowsUseEachVerifiedOwnerVersion(t *testing.T) {
	for _, versionFilter := range []bool{false, true} {
		t.Run(map[bool]string{false: "both_versions", true: "v2_rule"}[versionFilter], func(t *testing.T) {
			v1 := runtimeJob("work", "root")
			v2 := v1.DeepCopy()
			v2.SetAPIVersion("batch/v2")
			first, second := runtimePod("first", "first", v1), runtimePod("second", "second", v2)
			fixture := startConnected(t, []*unstructured.Unstructured{v1, v2, first, second}, func(options *RuntimeOptions) {
				options.Definitions.Catalog.Enabled = false
				if versionFilter {
					options.ResourceRules = []ResourceRule{{APIGroups: []string{"batch"}, APIVersions: []string{"v2"}, Resources: []string{"jobs"}}}
				}
			}, "")
			awaitRuntime(t, func() bool {
				view := fixture.store.Capture()
				if len(view.Records) != 1 {
					return false
				}
				record := view.Records[0]
				if record.Workload.HasStatus {
					return false
				}
				row, exists := record.Pod("second")
				valid, _ := view.Valid(row.Mapping)
				if !exists || !valid || row.Record.Workload.Version != "v2" {
					return false
				}
				row, exists = record.Pod("first")
				if versionFilter {
					return !exists
				}
				valid, _ = view.Valid(row.Mapping)
				return exists && valid && row.Record.Workload.Version == "v1"
			})
		})
	}
}
