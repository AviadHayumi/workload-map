// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func childNames(uid types.UID) (string, string, error) {
	pvc, service := "karta-"+string(uid)+"-data", "karta-"+string(uid)+"-service"
	if uid == "" || len(validation.IsDNS1123Subdomain(pvc)) != 0 || len(validation.IsDNS1035Label(service)) != 0 {
		return "", "", failure("InvalidInput", "workload UID cannot form valid child resource names")
	}
	return pvc, service, nil
}

func childMetadata(parent *unstructured.Unstructured, name string, service bool) metav1.ObjectMeta {
	metadata := metav1.ObjectMeta{
		Name: name, Namespace: parent.GetNamespace(),
		Labels:      map[string]string{managedByKey: "cel-plugins", workloadUIDKey: string(parent.GetUID())},
		Annotations: map[string]string{parentAPIVersionKey: parent.GetAPIVersion(), parentKindKey: parent.GetKind(), parentNameKey: parent.GetName()},
	}
	if service {
		controller, block := true, false
		metadata.OwnerReferences = []metav1.OwnerReference{{APIVersion: parent.GetAPIVersion(), Kind: parent.GetKind(), Name: parent.GetName(), UID: parent.GetUID(), Controller: &controller, BlockOwnerDeletion: &block}}
	}
	return metadata
}

func verifyChild(parent *unstructured.Unstructured, child client.Object, service bool) error {
	expected := childMetadata(parent, child.GetName(), service)
	if child.GetNamespace() != parent.GetNamespace() || child.GetUID() == "" {
		return failure("OwnershipConflict", "child namespace or UID is invalid")
	}
	for key, value := range expected.Labels {
		if child.GetLabels()[key] != value {
			return failure("OwnershipConflict", "child provenance labels do not match the workload")
		}
	}
	for key, value := range expected.Annotations {
		if child.GetAnnotations()[key] != value {
			return failure("OwnershipConflict", "child provenance annotations do not match the workload")
		}
	}
	if !reflect.DeepEqual(child.GetOwnerReferences(), expected.OwnerReferences) && (len(child.GetOwnerReferences()) != 0 || service) {
		return failure("OwnershipConflict", "child owner references do not match the workload")
	}
	return nil
}

func desiredPVC(parent *unstructured.Unstructured, name string, storage *storageState) *corev1.PersistentVolumeClaim {
	mode := corev1.PersistentVolumeFilesystem
	class := storage.StorageClass
	return &corev1.PersistentVolumeClaim{ObjectMeta: childMetadata(parent, name, false), Spec: corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &mode, StorageClassName: &class,
		Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(storage.Size)}},
	}}
}

func verifyPVC(parent *unstructured.Unstructured, pvc *corev1.PersistentVolumeClaim, storage *storageState) error {
	if err := verifyChild(parent, pvc, false); err != nil {
		return err
	}
	if storage.UID != "" && pvc.UID != storage.UID {
		return failure("OwnershipConflict", "claim UID differs from its committed identity")
	}
	if pvc.DeletionTimestamp != nil {
		return failure("StorageMissing", "claim is being deleted")
	}
	size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	expected := resource.MustParse(storage.Size)
	if size.Cmp(expected) != 0 || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != storage.StorageClass ||
		!reflect.DeepEqual(pvc.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}) ||
		pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
		return failure("StorageChangeUnsupported", "claim immutable intent does not match saved storage")
	}
	return nil
}

func validateService(parent *unstructured.Unstructured, service *corev1.Service) error {
	if err := verifyChild(parent, service, true); err != nil {
		return err
	}
	if service.Spec.Type != corev1.ServiceTypeClusterIP {
		return failure("ServiceConflict", "managed Service must have type ClusterIP")
	}
	count := 0
	for _, port := range service.Spec.Ports {
		if port.Name == "http" {
			count++
		}
	}
	if count > 1 {
		return failure("ServiceConflict", "managed Service contains duplicate http ports")
	}
	return nil
}

func serviceSpec(service *corev1.Service, parent *unstructured.Unstructured, intent *serviceIntent) {
	service.Spec.Selector = map[string]string{workloadUIDKey: string(parent.GetUID())}
	port := corev1.ServicePort{Name: "http", Protocol: corev1.ProtocolTCP, Port: intent.Port, TargetPort: intstr.FromInt32(intent.TargetPort)}
	for i := range service.Spec.Ports {
		if service.Spec.Ports[i].Name == "http" {
			service.Spec.Ports[i].Port = port.Port
			service.Spec.Ports[i].TargetPort = port.TargetPort
			service.Spec.Ports[i].Protocol = port.Protocol
			return
		}
	}
	service.Spec.Ports = append(service.Spec.Ports, port)
}

func (r *reconciler) reconcileService(ctx context.Context, parent *unstructured.Unstructured, name string, intent *serviceIntent) error {
	service := &corev1.Service{}
	err := r.reader.Get(ctx, types.NamespacedName{Namespace: parent.GetNamespace(), Name: name}, service)
	if apierrors.IsNotFound(err) {
		if intent == nil {
			return nil
		}
		if err := r.guardParent(ctx, parent); err != nil {
			return err
		}
		service = &corev1.Service{ObjectMeta: childMetadata(parent, name, true), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}}
		serviceSpec(service, parent, intent)
		return r.client.Create(ctx, service)
	}
	if err != nil {
		return err
	}
	if err := validateService(parent, service); err != nil {
		return err
	}
	if service.DeletionTimestamp != nil {
		return failure("ServiceDeleting", "managed Service is being deleted")
	}
	if intent == nil {
		if err := r.guardParent(ctx, parent); err != nil {
			return err
		}
		uid, rv := service.UID, service.ResourceVersion
		err := r.client.Delete(ctx, service, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		remaining := &corev1.Service{}
		err = r.reader.Get(ctx, client.ObjectKeyFromObject(service), remaining)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := validateService(parent, remaining); err != nil {
			return err
		}
		return failure("ServiceDeleting", "managed Service deletion has not completed")
	}
	base := service.DeepCopy()
	serviceSpec(service, parent, intent)
	if reflect.DeepEqual(base.Spec, service.Spec) {
		return nil
	}
	if err := r.guardParent(ctx, parent); err != nil {
		return err
	}
	return r.client.Patch(ctx, service, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}
