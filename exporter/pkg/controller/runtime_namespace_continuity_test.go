// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRuntimeNamespaceMetadataPreservesScopeEvidence(t *testing.T) {
	for _, transition := range []string{"scope-exit", "replacement"} {
		t.Run(transition, func(t *testing.T) {
			other := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{
					"name": "other", "uid": "other-namespace", "resourceVersion": "1",
				},
			}}
			root := runtimeJob("work", "root")
			fixture := startConnected(t, []*unstructured.Unstructured{root, runtimePod("worker", "pod", root), other}, func(options *RuntimeOptions) {
				options.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "team"}}
			}, "")
			awaitRuntime(t, func() bool {
				view, record := invariantPublication(fixture, "root")
				if record == nil {
					return false
				}
				valid, _ := view.Valid(record.Status)
				return valid
			})
			fixture.controller.queue.ShutDownWithDrain()
			fixture.controller.mu.Lock()
			beforeScope := fixture.controller.scopeRevision
			beforeEvidence := fixture.controller.namespaceEvidence
			team := fixture.controller.namespaceObjects["team"].DeepCopy()
			fixture.controller.mu.Unlock()
			fixture.now.Add(int64(time.Minute))

			for _, namespace := range []*unstructured.Unstructured{team, other} {
				namespace.SetResourceVersion("2")
				namespace.SetAnnotations(map[string]string{"description": "unrelated metadata"})
				fixture.controller.acceptNamespace(namespace, false)
				fixture.controller.mu.Lock()
				scope := fixture.controller.scopeRevision
				evidence := fixture.controller.namespaceEvidence
				stored := fixture.controller.namespaceObjects[namespace.GetName()]
				fixture.controller.mu.Unlock()
				if scope != beforeScope || !reflect.DeepEqual(evidence, beforeEvidence) {
					t.Fatalf("unrelated namespace update changed scope or renewed/revoked evidence: scope %d -> %d; evidence %+v -> %+v", beforeScope, scope, beforeEvidence, evidence)
				}
				if stored.GetResourceVersion() != "2" {
					t.Fatal("namespace observation did not advance")
				}
				view, record := invariantPublication(fixture, "root")
				valid, _ := view.Valid(record.Status)
				if !valid {
					t.Fatal("unrelated namespace update invalidated root status")
				}
			}

			changed := team.DeepCopy()
			changed.SetResourceVersion("3")
			if transition == "replacement" {
				changed.SetUID("new-namespace")
			} else {
				changed.SetLabels(nil)
			}
			fixture.controller.acceptNamespace(changed, false)
			if fixture.store.Published("root") != nil {
				t.Fatalf("%s retained old namespace publication", transition)
			}
			fixture.controller.mu.Lock()
			afterScope := fixture.controller.scopeRevision
			fixture.controller.mu.Unlock()
			if afterScope == beforeScope {
				t.Fatalf("%s did not invalidate scope", transition)
			}
		})
	}
}
