// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	record "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type testClient struct {
	client.Client
	patches, creates, deletes int
	beforePatch               func(client.Object, client.Patch) error
	afterPatch                func(client.Object) error
	beforeCreate              func(client.Object) error
	afterCreate               func(client.Object) error
	beforeDelete              func(client.Object, ...client.DeleteOption) error
}

func (c *testClient) Patch(ctx context.Context, o client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.patches++
	if c.beforePatch != nil {
		if err := c.beforePatch(o, patch); err != nil {
			return err
		}
	}
	if err := c.Client.Patch(ctx, o, patch, opts...); err != nil {
		return err
	}
	if c.afterPatch != nil {
		return c.afterPatch(o)
	}
	return nil
}

func (c *testClient) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	c.creates++
	if c.beforeCreate != nil {
		if err := c.beforeCreate(o); err != nil {
			return err
		}
	}
	// The fake client does not implement API-server UID allocation.
	if o.GetUID() == "" {
		o.SetUID(types.UID(fmt.Sprintf("created-%d", c.creates)))
	}
	if err := c.Client.Create(ctx, o, opts...); err != nil {
		return err
	}
	if c.afterCreate != nil {
		return c.afterCreate(o)
	}
	return nil
}

func (c *testClient) Delete(ctx context.Context, o client.Object, opts ...client.DeleteOption) error {
	c.deletes++
	if c.beforeDelete != nil {
		if err := c.beforeDelete(o, opts...); err != nil {
			return err
		}
	}
	return c.Client.Delete(ctx, o, opts...)
}

func testWorkload() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "test", "uid": "12345678-1234-1234-1234-123456789abc", "resourceVersion": "1"},
		"spec": map[string]any{
			"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
				"spec":     map[string]any{"containers": []any{map[string]any{"name": "web", "image": "ghcr.io/example/web:v1.0.0", "ports": []any{map[string]any{"containerPort": int64(8080)}}}}},
			},
		},
	}}
}

func newTestReconciler(t *testing.T, objects ...client.Object) (*reconciler, *testClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := &testClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()}
	p, err := newPrograms()
	if err != nil {
		t.Fatal(err)
	}
	return &reconciler{client: c, reader: c.Client, namespace: "test", gvk: appsv1.SchemeGroupVersion.WithKind("Deployment"), programs: p, recorder: record.NewFakeRecorder(100)}, c
}

func runReconcile(t *testing.T, r *reconciler) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test", Name: "web"}})
	return err
}

func fetchWorkload(t *testing.T, c client.Reader) *unstructured.Unstructured {
	t.Helper()
	o := testWorkload()
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(o), o); err != nil {
		t.Fatal(err)
	}
	return o
}

func assertResult(t *testing.T, c client.Reader, status, reason string) {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(fetchWorkload(t, c).GetAnnotations()[annotationResult]), &result); err != nil {
		t.Fatal(err)
	}
	if result["state"] != status || (reason != "" && result["reason"] != reason) {
		t.Fatalf("result = %v, want %s/%s", result, status, reason)
	}
}

func persistedState(t *testing.T, c client.Reader) state {
	t.Helper()
	s, err := readState(fetchWorkload(t, c))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReconcileStableSuccessAndInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		status      string
	}{
		{name: "success", annotations: map[string]string{annotationPool: "general", annotationService: "80", annotationStorage: "1Gi"}, status: "Applied"},
		{name: "invalid", annotations: map[string]string{annotationPool: "general", annotationService: "bad", annotationStorage: "1Gi"}, status: "Error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testWorkload()
			o.SetAnnotations(tc.annotations)
			r, c := newTestReconciler(t, o)
			if err := runReconcile(t, r); err != nil {
				t.Fatal(err)
			}
			assertResult(t, c, tc.status, "")
			patches, creates, deletes := c.patches, c.creates, c.deletes
			for range 3 {
				if err := runReconcile(t, r); err != nil {
					t.Fatal(err)
				}
			}
			if c.patches != patches || c.creates != creates || c.deletes != deletes {
				t.Fatalf("repeat reconcile wrote: patches %d->%d, creates %d->%d, deletes %d->%d", patches, c.patches, creates, c.creates, deletes, c.deletes)
			}
			if tc.status == "Error" && (c.creates != 0 || persistedState(t, c).Storage != nil) {
				t.Fatal("invalid plugin input caused effects")
			}
		})
	}
}

func TestStorageDurableWriteBoundaries(t *testing.T) {
	lost := errors.New("injected unavailable response")
	for _, tc := range []struct {
		name, fault     string
		expectClaim     bool
		expectCommitted bool
		removeClaim     bool
	}{
		{name: "reservation rejected", fault: "reservation-before"},
		{name: "reservation response lost", fault: "reservation-after"},
		{name: "create failed", fault: "create-before"},
		{name: "successful create response lost", fault: "create-after", expectClaim: true, expectCommitted: true},
		{name: "lost create followed by claim disappearance", fault: "create-after", removeClaim: true},
		{name: "UID commitment failed", fault: "commit-before", expectClaim: true, expectCommitted: true},
		{name: "UID commitment response lost", fault: "commit-after", expectClaim: true, expectCommitted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testWorkload()
			o.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
			r, c := newTestReconciler(t, o)
			fired := false
			c.beforePatch = func(obj client.Object, patch client.Patch) error {
				data, err := patch.Data(obj)
				if err != nil {
					return err
				}
				var payload map[string]any
				if err := json.Unmarshal(data, &payload); err != nil {
					return err
				}
				if _, ok := payload["metadata"]; !ok {
					return nil
				}
				u, ok := obj.(*unstructured.Unstructured)
				if !ok {
					return nil
				}
				s, err := readState(u)
				if err != nil {
					return err
				}
				if !fired && s.Storage != nil && ((tc.fault == "reservation-before" && s.Storage.UID == "") || (tc.fault == "commit-before" && s.Storage.UID != "")) {
					fired = true
					return lost
				}
				return nil
			}
			c.afterPatch = func(obj client.Object) error {
				if !fired && (tc.fault == "reservation-after" || tc.fault == "commit-after") {
					if u, ok := obj.(*unstructured.Unstructured); ok {
						s, err := readState(u)
						if err != nil {
							return err
						}
						if s.Storage != nil && ((tc.fault == "reservation-after" && s.Storage.UID == "") || (tc.fault == "commit-after" && s.Storage.UID != "")) {
							fired = true
							return lost
						}
					}
				}
				return nil
			}
			c.beforeCreate = func(obj client.Object) error {
				if _, ok := obj.(*corev1.PersistentVolumeClaim); ok && !fired && tc.fault == "create-before" {
					fired = true
					return lost
				}
				return nil
			}
			c.afterCreate = func(obj client.Object) error {
				if _, ok := obj.(*corev1.PersistentVolumeClaim); ok && !fired && tc.fault == "create-after" {
					fired = true
					return lost
				}
				return nil
			}
			_ = runReconcile(t, r)
			if !fired {
				t.Fatal("fault boundary was never reached")
			}
			c.beforePatch, c.afterPatch, c.beforeCreate, c.afterCreate = nil, nil, nil, nil
			if tc.removeClaim {
				name, _, err := childNames(o.GetUID())
				if err != nil {
					t.Fatal(err)
				}
				claim := &corev1.PersistentVolumeClaim{}
				if err := c.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: name}, claim); err != nil {
					t.Fatal(err)
				}
				if err := c.Client.Delete(context.Background(), claim); err != nil {
					t.Fatal(err)
				}
			}
			// Reconstruct using only persisted objects and the same API client.
			restarted := &reconciler{client: c, reader: c.Client, namespace: r.namespace, storageClass: r.storageClass, gvk: r.gvk, programs: r.programs, recorder: r.recorder}
			creates := c.creates
			if err := runReconcile(t, restarted); err != nil {
				t.Fatal(err)
			}
			if tc.fault == "reservation-before" {
				assertResult(t, c, "Applied", "")
				return
			}
			if c.creates != creates {
				t.Fatalf("restart retried Create: %d -> %d", creates, c.creates)
			}
			s := persistedState(t, c)
			if s.Storage == nil {
				t.Fatal("reservation disappeared")
			}
			if tc.expectCommitted {
				if s.Storage.UID == "" {
					t.Fatal("matching persisted claim was not committed")
				}
				assertResult(t, c, "Applied", "")
			} else {
				if s.Storage.UID != "" {
					t.Fatal("absent claim was committed")
				}
				assertResult(t, c, "Error", "StorageCreationUncertain")
			}
			pvcName, _, err := childNames(o.GetUID())
			if err != nil {
				t.Fatal(err)
			}
			claim := &corev1.PersistentVolumeClaim{}
			err = c.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: pvcName}, claim)
			if tc.expectClaim != (err == nil) {
				t.Fatalf("claim existence: %v", err)
			}
		})
	}
}

func TestParentPatchRequiresOptimisticResourceVersion(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationPool: "general"})
	r, c := newTestReconciler(t, o)
	fired := false
	c.beforePatch = func(obj client.Object, patch client.Patch) error {
		if fired {
			return nil
		}
		data, err := patch.Data(obj)
		if err != nil {
			return err
		}
		var payload struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		if payload.Metadata.ResourceVersion == "" {
			t.Fatal("parent patch lacks optimistic resourceVersion")
		}
		fired = true
		return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, obj.GetName(), errors.New("injected concurrent edit"))
	}
	if err := runReconcile(t, r); !apierrors.IsConflict(err) {
		t.Fatalf("conflict error = %v", err)
	}
	got := fetchWorkload(t, c)
	if _, found, _ := unstructured.NestedFieldNoCopy(got.Object, "spec", "template", "spec", "nodeSelector", poolKey); found {
		t.Fatal("rejected parent patch changed template")
	}
	if c.creates != 0 {
		t.Fatal("child created after stale parent patch")
	}
	c.beforePatch = nil
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Applied", "")
}

func TestDeletingOrUnrequestedParentHasNoEffects(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleting=%v", deleting), func(t *testing.T) {
			o := testWorkload()
			if deleting {
				now := metav1.Now()
				o.SetDeletionTimestamp(&now)
				o.SetFinalizers([]string{"example.test/hold"})
				o.SetAnnotations(map[string]string{annotationStorage: "1Gi", annotationService: "80"})
			}
			r, c := newTestReconciler(t, o)
			before := fetchWorkload(t, c)
			if err := runReconcile(t, r); err != nil {
				t.Fatal(err)
			}
			if c.creates+c.patches+c.deletes != 0 || !reflect.DeepEqual(before, fetchWorkload(t, c)) {
				t.Fatal("inactive parent caused effects")
			}
		})
	}
}

func TestStorageOptOutAfterReservationPreventsCreate(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
	r, c := newTestReconciler(t, o)
	fired := false
	c.afterPatch = func(obj client.Object) error {
		if fired {
			return nil
		}
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil
		}
		s, err := readState(u)
		if err != nil {
			return err
		}
		if s.Storage == nil {
			return nil
		}
		fired = true
		fresh := fetchWorkload(t, c)
		annotations := fresh.GetAnnotations()
		delete(annotations, annotationStorage)
		fresh.SetAnnotations(annotations)
		return c.Update(context.Background(), fresh)
	}
	if err := runReconcile(t, r); !apierrors.IsConflict(err) {
		t.Fatalf("concurrent change error = %v", err)
	}
	if !fired || c.creates != 0 {
		t.Fatal("claim created after reservation's parent changed")
	}
	c.afterPatch = nil
	fresh := fetchWorkload(t, c)
	annotations := fresh.GetAnnotations()
	annotations[annotationStorage] = "1Gi"
	fresh.SetAnnotations(annotations)
	if err := c.Update(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Error", "StorageCreationUncertain")
	if c.creates != 0 {
		t.Fatal("later reconciliation created an uncertain claim")
	}
}

func TestReconcilePersistsRawTemplateFields(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationPool: "general"})
	if err := unstructured.SetNestedField(o.Object, int64(9007199254740993), "spec", "template", "future", "largeInteger"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(o.Object, "keep", "spec", "template", "spec", "nodeSelector", "example.test/user"); err != nil {
		t.Fatal(err)
	}
	r, c := newTestReconciler(t, o)
	before, _, _ := unstructured.NestedMap(fetchWorkload(t, c).Object, "spec", "template")
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	current := fetchWorkload(t, c)
	annotations := current.GetAnnotations()
	delete(annotations, annotationPool)
	current.SetAnnotations(annotations)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	after, _, _ := unstructured.NestedMap(fetchWorkload(t, c).Object, "spec", "template")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("raw persisted template changed:\nbefore %#v\nafter %#v", before, after)
	}
}

func TestStatusOnlyUpdateAfterReservationAllowsCreate(t *testing.T) {
	parent := testWorkload()
	parent.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
	r, c := newTestReconciler(t, parent)
	c.afterPatch = func(client.Object) error {
		c.afterPatch = nil
		current := fetchWorkload(t, c)
		current.Object["status"] = map[string]any{"observedGeneration": int64(1)}
		return c.Status().Update(context.Background(), current)
	}
	// The original parent patch base can conflict after the status update. The
	// successful creation is recovered by observation on the next reconciliation.
	_ = runReconcile(t, r)
	if c.creates != 1 {
		t.Fatalf("status update prevented initial Create: %d calls", c.creates)
	}
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Applied", "")
	if saved := persistedState(t, c); saved.Storage == nil || saved.Storage.UID == "" {
		t.Fatalf("claim identity was not committed: %+v", saved)
	}
	if c.creates != 1 {
		t.Fatal("recovery retried Create")
	}
}

func TestChangedStorageRequestAfterReservationBlocksCreate(t *testing.T) {
	parent := testWorkload()
	parent.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
	r, c := newTestReconciler(t, parent)
	c.afterPatch = func(client.Object) error {
		c.afterPatch = nil
		current := fetchWorkload(t, c)
		annotations := current.GetAnnotations()
		annotations[annotationStorage] = "2Gi"
		current.SetAnnotations(annotations)
		return c.Update(context.Background(), current)
	}
	_ = runReconcile(t, r)
	if c.creates != 0 {
		t.Fatal("changed request consumed initial Create permit")
	}
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Error", "StorageChangeUnsupported")
	if c.creates != 0 {
		t.Fatal("later reconcile retried changed request")
	}
	if saved := persistedState(t, c); saved.Storage == nil || saved.Storage.Size != "1Gi" || saved.Storage.UID != "" {
		t.Fatalf("original reservation changed: %+v", saved)
	}
}
