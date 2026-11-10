// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestClaimImmutableIntentAndBindingFields(t *testing.T) {
	parent := testWorkload()
	saved := &storageState{Size: "1Gi", StorageClass: "", UID: "claim-uid"}
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.PersistentVolumeClaim)
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "binding defaults", valid: true, mutate: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.VolumeName = "bound-volume"
			p.Status.Phase = corev1.ClaimBound
		}},
		{name: "replacement UID", mutate: func(p *corev1.PersistentVolumeClaim) { p.UID = "replacement" }},
		{name: "wrong capacity", mutate: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("2Gi")
		}},
		{name: "default class instead of explicit empty", mutate: func(p *corev1.PersistentVolumeClaim) { p.Spec.StorageClassName = nil }},
		{name: "wrong class", mutate: func(p *corev1.PersistentVolumeClaim) { value := "default"; p.Spec.StorageClassName = &value }},
		{name: "wrong access", mutate: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
		}},
		{name: "wrong mode", mutate: func(p *corev1.PersistentVolumeClaim) {
			value := corev1.PersistentVolumeBlock
			p.Spec.VolumeMode = &value
		}},
		{name: "deleting", mutate: func(p *corev1.PersistentVolumeClaim) { now := metav1.Now(); p.DeletionTimestamp = &now }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pvc := desiredPVC(parent, "claim", saved)
			pvc.UID = saved.UID
			if tc.mutate != nil {
				tc.mutate(pvc)
			}
			before := pvc.DeepCopy()
			if err := verifyPVC(parent, pvc, saved); (err == nil) != tc.valid {
				t.Fatalf("verifyPVC() = %v, valid = %v", err, tc.valid)
			}
			if !reflect.DeepEqual(before, pvc) {
				t.Fatal("PVC validation mutated claim")
			}
		})
	}
}

func TestServiceSchemaConflicts(t *testing.T) {
	parent := testWorkload()
	for _, mode := range []string{"non-ClusterIP", "duplicate http"} {
		t.Run(mode, func(t *testing.T) {
			svc := &corev1.Service{ObjectMeta: childMetadata(parent, "service", true), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}}
			svc.UID = "service-uid"
			if mode == "non-ClusterIP" {
				svc.Spec.Type = corev1.ServiceTypeNodePort
			} else {
				svc.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: 80}, {Name: "http", Port: 81}}
			}
			if err := validateService(parent, svc); err == nil {
				t.Fatal("conflicting service accepted")
			}
		})
	}
}

func TestServiceDeletionUsesIdentityPreconditionsBeforeLabelRelease(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationService: "80"})
	r, c := newTestReconciler(t, o)
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	current := fetchWorkload(t, c)
	annotations := current.GetAnnotations()
	delete(annotations, annotationService)
	current.SetAnnotations(annotations)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	fired := false
	c.beforeDelete = func(obj client.Object, opts ...client.DeleteOption) error {
		fired = true
		options := &client.DeleteOptions{}
		for _, option := range opts {
			option.ApplyToDelete(options)
		}
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != obj.GetUID() || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != obj.GetResourceVersion() {
			t.Fatal("Service delete lacks exact UID/resourceVersion preconditions")
		}
		if !persistedState(t, c).Service {
			t.Fatal("template label ownership released before Service delete")
		}
		return errors.New("injected delete failure")
	}
	if err := runReconcile(t, r); err == nil {
		t.Fatal("delete failure not returned")
	}
	if !fired || !persistedState(t, c).Service {
		t.Fatal("Service ownership lost on delete failure")
	}
	c.beforeDelete = nil
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	if persistedState(t, c).Service {
		t.Fatal("Service ownership not released after successful deletion")
	}
}

func TestChildIdentityRequiresExactProvenance(t *testing.T) {
	parent := testWorkload()
	for _, service := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			mutate func(*metav1.ObjectMeta)
		}{
			{name: "valid"},
			{name: "missing UID", mutate: func(m *metav1.ObjectMeta) { m.UID = "" }},
			{name: "wrong namespace", mutate: func(m *metav1.ObjectMeta) { m.Namespace = "elsewhere" }},
			{name: "missing marker", mutate: func(m *metav1.ObjectMeta) { delete(m.Labels, managedByKey) }},
			{name: "wrong parent UID", mutate: func(m *metav1.ObjectMeta) { m.Labels[workloadUIDKey] = "another-uid" }},
			{name: "wrong API version", mutate: func(m *metav1.ObjectMeta) { m.Annotations[parentAPIVersionKey] = "apps/v1beta1" }},
			{name: "wrong kind", mutate: func(m *metav1.ObjectMeta) { m.Annotations[parentKindKey] = "CronJob" }},
			{name: "wrong parent name", mutate: func(m *metav1.ObjectMeta) { m.Annotations[parentNameKey] = "other" }},
			{name: "foreign owner", mutate: func(m *metav1.ObjectMeta) {
				m.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "foreign", UID: "foreign"}}
			}},
		} {
			t.Run(map[bool]string{false: "PVC", true: "Service"}[service]+"/"+tc.name, func(t *testing.T) {
				child := &corev1.Service{ObjectMeta: childMetadata(parent, "child", service)}
				child.UID = "child-uid"
				if tc.mutate != nil {
					tc.mutate(&child.ObjectMeta)
				}
				err := verifyChild(parent, child, service)
				if (err == nil) != (tc.mutate == nil) {
					t.Fatalf("verifyChild() = %v", err)
				}
			})
		}
	}
	for _, field := range []string{"controller", "blockOwnerDeletion", "uid", "name", "kind", "apiVersion", "missing owner"} {
		t.Run("Service owner/"+field, func(t *testing.T) {
			child := &corev1.Service{ObjectMeta: childMetadata(parent, "child", true)}
			child.UID = "child-uid"
			owner := &child.OwnerReferences[0]
			switch field {
			case "controller":
				value := false
				owner.Controller = &value
			case "blockOwnerDeletion":
				value := true
				owner.BlockOwnerDeletion = &value
			case "uid":
				owner.UID = "other"
			case "name":
				owner.Name = "other"
			case "kind":
				owner.Kind = "Job"
			case "apiVersion":
				owner.APIVersion = "batch/v1"
			case "missing owner":
				child.OwnerReferences = nil
			}
			if err := verifyChild(parent, child, true); err == nil {
				t.Fatal("inexact controller owner accepted")
			}
		})
	}
}

func TestServiceConvergesOwnedFieldsAndPreservesDefaults(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationService: "80"})
	_, name, err := childNames(o.GetUID())
	if err != nil {
		t.Fatal(err)
	}
	appProtocol := "http"
	policy := corev1.IPFamilyPolicySingleStack
	svc := &corev1.Service{ObjectMeta: childMetadata(o, name, true), Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.10", ClusterIPs: []string{"10.96.0.10"}, IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol}, IPFamilyPolicy: &policy,
		SessionAffinity: corev1.ServiceAffinityNone,
		Selector:        map[string]string{"drift": "wrong"},
		Ports:           []corev1.ServicePort{{Name: "http", Port: 81, TargetPort: intstr.FromInt32(9000), Protocol: corev1.ProtocolUDP, AppProtocol: &appProtocol}, {Name: "metrics", Port: 9090, TargetPort: intstr.FromInt32(9090), Protocol: corev1.ProtocolTCP}},
	}}
	svc.UID = "service-uid"
	svc.ResourceVersion = "1"
	svc.Labels["user"] = "keep"
	svc.Annotations["user"] = "keep"
	before := svc.DeepCopy()
	r, c := newTestReconciler(t, o, svc)
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	got := &corev1.Service{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(svc), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Selector[workloadUIDKey] != string(o.GetUID()) || len(got.Spec.Selector) != 1 || got.Spec.Ports[0].Port != 80 || got.Spec.Ports[0].TargetPort != intstr.FromInt32(8080) || got.Spec.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("owned service fields did not converge: %+v", got.Spec)
	}
	before.Spec.Selector = got.Spec.Selector
	before.Spec.Ports[0].Port = 80
	before.Spec.Ports[0].TargetPort = intstr.FromInt32(8080)
	before.Spec.Ports[0].Protocol = corev1.ProtocolTCP
	if !reflect.DeepEqual(before.Spec, got.Spec) || !reflect.DeepEqual(before.Labels, got.Labels) || !reflect.DeepEqual(before.Annotations, got.Annotations) || got.UID != before.UID {
		t.Fatal("Service reconciliation changed unrelated fields")
	}
	patches := c.patches
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	if c.patches != patches {
		t.Fatal("converged Service caused another patch")
	}
	if err := c.Client.Delete(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(svc), got); err != nil {
		t.Fatalf("deleted Service not recreated: %v", err)
	}
}

func TestCommittedStorageIsNeverReplacedOrResized(t *testing.T) {
	for _, mode := range []string{"missing", "replacement UID", "resize", "class change"} {
		t.Run(mode, func(t *testing.T) {
			o := testWorkload()
			o.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
			r, c := newTestReconciler(t, o)
			if err := runReconcile(t, r); err != nil {
				t.Fatal(err)
			}
			saved := persistedState(t, c)
			name, _, err := childNames(o.GetUID())
			if err != nil {
				t.Fatal(err)
			}
			claim := &corev1.PersistentVolumeClaim{}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: name}, claim); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing", "replacement UID":
				if err := c.Client.Delete(context.Background(), claim); err != nil {
					t.Fatal(err)
				}
				if mode == "replacement UID" {
					claim.UID = "replacement"
					claim.ResourceVersion = ""
					if err := c.Client.Create(context.Background(), claim); err != nil {
						t.Fatal(err)
					}
				}
			case "resize":
				current := fetchWorkload(t, c)
				annotations := current.GetAnnotations()
				annotations[annotationStorage] = "2Gi"
				current.SetAnnotations(annotations)
				if err := c.Update(context.Background(), current); err != nil {
					t.Fatal(err)
				}
			case "class change":
				r.storageClass = "different"
			}
			creates := c.creates
			if err := runReconcile(t, r); err != nil {
				t.Fatal(err)
			}
			reason := "StorageChangeUnsupported"
			if mode == "missing" {
				reason = "StorageMissing"
			}
			if mode == "replacement UID" {
				reason = "OwnershipConflict"
			}
			assertResult(t, c, "Error", reason)
			if c.creates != creates || !reflect.DeepEqual(saved.Storage, persistedState(t, c).Storage) {
				t.Fatal("committed storage intent changed")
			}
		})
	}
}

func TestPendingOptOutAndReenable(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
	r, c := newTestReconciler(t, o)
	c.beforeCreate = func(client.Object) error { return errors.New("injected Create failure") }
	if err := runReconcile(t, r); err == nil {
		t.Fatal("Create failure not returned")
	}
	c.beforeCreate = nil
	current := fetchWorkload(t, c)
	annotations := current.GetAnnotations()
	delete(annotations, annotationStorage)
	current.SetAnnotations(annotations)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	creates := c.creates
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Applied", "")
	if s := persistedState(t, c); s.Storage == nil || s.Storage.UID != "" {
		t.Fatal("pending state was lost on opt-out")
	}
	current = fetchWorkload(t, c)
	annotations = current.GetAnnotations()
	annotations[annotationStorage] = "1Gi"
	current.SetAnnotations(annotations)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Error", "StorageCreationUncertain")
	if c.creates != creates {
		t.Fatal("pending opt-in retried Create")
	}
}

func TestServiceFailureRetainsParentAndClaimCommit(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationStorage: "1Gi", annotationPool: "general", annotationService: "80"})
	r, c := newTestReconciler(t, o)
	c.beforeCreate = func(obj client.Object) error {
		if _, ok := obj.(*corev1.Service); ok {
			return errors.New("injected Service failure")
		}
		return nil
	}
	if err := runReconcile(t, r); err == nil {
		t.Fatal("Service Create failure not returned")
	}
	s := persistedState(t, c)
	if s.Storage == nil || s.Storage.UID == "" || s.Pool != "general" || !s.Service {
		t.Fatalf("completed parent writes lost after Service error: %+v", s)
	}
	c.beforeCreate = nil
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Applied", "")
	if !reflect.DeepEqual(s, persistedState(t, c)) {
		t.Fatal("retry changed committed state")
	}
}

func TestForeignChildCollisionPreventsEffects(t *testing.T) {
	for _, kind := range []string{"PVC", "Service"} {
		t.Run(kind, func(t *testing.T) {
			o := testWorkload()
			o.SetAnnotations(map[string]string{annotationStorage: "1Gi", annotationPool: "general", annotationService: "80"})
			pvcName, svcName, err := childNames(o.GetUID())
			if err != nil {
				t.Fatal(err)
			}
			var foreign client.Object = &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: "test", UID: "foreign"}}
			if kind == "PVC" {
				foreign = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: "test", UID: "foreign"}}
			}
			r, c := newTestReconciler(t, o, foreign)
			if err := runReconcile(t, r); err != nil {
				t.Fatal(err)
			}
			assertResult(t, c, "Error", "OwnershipConflict")
			if c.creates != 0 || c.deletes != 0 || persistedState(t, c).Storage != nil {
				t.Fatal("foreign collision caused effects")
			}
			got := foreign.DeepCopyObject().(client.Object)
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(foreign), got); err != nil {
				t.Fatal(err)
			}
			if got.GetUID() != "foreign" {
				t.Fatal("foreign child was replaced")
			}
		})
	}
}

func TestOptOutRetainsClaimAndDeletesService(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationStorage: "1Gi", annotationPool: "general", annotationService: "80"})
	r, c := newTestReconciler(t, o)
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	s := persistedState(t, c)
	current := fetchWorkload(t, c)
	annotations := current.GetAnnotations()
	delete(annotations, annotationStorage)
	delete(annotations, annotationPool)
	delete(annotations, annotationService)
	current.SetAnnotations(annotations)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Applied", "")
	gotState := persistedState(t, c)
	if !reflect.DeepEqual(gotState.Storage, s.Storage) || gotState.Pool != "" || gotState.Service {
		t.Fatalf("opt-out state = %+v", gotState)
	}
	pvcName, svcName, err := childNames(o.GetUID())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: pvcName}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: svcName}, &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Service still exists: %v", err)
	}
}

func TestPendingClaimCommitsAfterOptOut(t *testing.T) {
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
	r, c := newTestReconciler(t, o)
	c.afterCreate = func(client.Object) error { return errors.New("injected lost Create response") }
	if err := runReconcile(t, r); err == nil {
		t.Fatal("lost Create response not returned")
	}
	c.afterCreate = nil
	current := fetchWorkload(t, c)
	annotations := current.GetAnnotations()
	delete(annotations, annotationStorage)
	current.SetAnnotations(annotations)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	creates := c.creates
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Applied", "")
	if saved := persistedState(t, c); saved.Storage == nil || saved.Storage.UID == "" {
		t.Fatal("existing pending claim not committed while disabled")
	}
	if c.creates != creates {
		t.Fatal("disabled storage issued Create")
	}
}

func TestClaimAlreadyExistsRecoveryAndPreflightRejection(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "preexisting matching claim rejected", true: "AlreadyExists observed after reservation"}[race], func(t *testing.T) {
			o := testWorkload()
			o.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
			name, _, err := childNames(o.GetUID())
			if err != nil {
				t.Fatal(err)
			}
			pvc := desiredPVC(o, name, &storageState{Size: "1Gi", StorageClass: ""})
			pvc.UID = "observed-claim"
			r, c := newTestReconciler(t, o)
			if race {
				c.beforeCreate = func(obj client.Object) error {
					if err := c.Client.Create(context.Background(), pvc); err != nil {
						return err
					}
					return apierrors.NewAlreadyExists(corev1.Resource("persistentvolumeclaims"), obj.GetName())
				}
			} else if err := c.Client.Create(context.Background(), pvc); err != nil {
				t.Fatal(err)
			}
			if err := runReconcile(t, r); err != nil {
				t.Fatal(err)
			}
			if race {
				assertResult(t, c, "Applied", "")
				if saved := persistedState(t, c); saved.Storage == nil || saved.Storage.UID != pvc.UID {
					t.Fatal("AlreadyExists claim not committed from live read")
				}
				if c.creates != 1 {
					t.Fatalf("Create attempts = %d", c.creates)
				}
			} else {
				assertResult(t, c, "Error", "OwnershipConflict")
				if c.creates != 0 {
					t.Fatal("preexisting claim triggered Create")
				}
			}
		})
	}
}

func TestServiceOptOutWaitsForFinalizer(t *testing.T) {
	parent := testWorkload()
	parent.SetAnnotations(map[string]string{annotationService: "80"})
	r, c := newTestReconciler(t, parent)
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	_, name, err := childNames(parent.GetUID())
	if err != nil {
		t.Fatal(err)
	}
	service := &corev1.Service{}
	key := types.NamespacedName{Namespace: parent.GetNamespace(), Name: name}
	if err := c.Get(context.Background(), key, service); err != nil {
		t.Fatal(err)
	}
	service.Finalizers = []string{"example.test/hold"}
	if err := c.Update(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	current := fetchWorkload(t, c)
	annotations := current.GetAnnotations()
	delete(annotations, annotationService)
	current.SetAnnotations(annotations)
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := runReconcile(t, r); err != nil {
			t.Fatal(err)
		}
		assertResult(t, c, "Error", "ServiceDeleting")
		if !persistedState(t, c).Service {
			t.Fatal("Service state released before deletion completed")
		}
		current = fetchWorkload(t, c)
		value, found, err := unstructured.NestedString(current.Object, "spec", "template", "metadata", "labels", workloadUIDKey)
		if err != nil || !found || value != string(parent.GetUID()) {
			t.Fatal("template UID label released while Service exists")
		}
	}
	if err := c.Get(context.Background(), key, service); err != nil {
		t.Fatal(err)
	}
	if service.DeletionTimestamp == nil {
		t.Fatal("Service deletion was not requested")
	}
	service.Finalizers = nil
	if err := c.Update(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	if err := runReconcile(t, r); err != nil {
		t.Fatal(err)
	}
	assertResult(t, c, "Applied", "")
	if persistedState(t, c).Service {
		t.Fatal("Service state retained after confirmed deletion")
	}
	current = fetchWorkload(t, c)
	if _, found, _ := unstructured.NestedString(current.Object, "spec", "template", "metadata", "labels", workloadUIDKey); found {
		t.Fatal("template UID label retained after confirmed deletion")
	}
}
