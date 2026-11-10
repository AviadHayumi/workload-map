// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type reconciler struct {
	client       client.Client
	reader       client.Reader
	namespace    string
	storageClass string
	gvk          schema.GroupVersionKind
	programs     *programs
	recorder     events.EventRecorder
}
type resultAnnotation struct {
	State     string `json:"state"`
	InputHash string `json:"inputHash"`
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (r *reconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	if request.Namespace != r.namespace {
		return ctrl.Result{}, nil
	}
	parent := &unstructured.Unstructured{}
	parent.SetGroupVersionKind(r.gvk)
	if err := r.reader.Get(ctx, request.NamespacedName, parent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if parent.GetDeletionTimestamp() != nil {
		return ctrl.Result{}, nil
	}
	annotations := parent.GetAnnotations()
	active := false
	for _, key := range []string{annotationStorage, annotationPool, annotationService, annotationState} {
		if _, ok := annotations[key]; ok {
			active = true
		}
	}
	if !active {
		return ctrl.Result{}, nil
	}
	periodic := ctrl.Result{RequeueAfter: 30 * time.Second}
	hash := inputHash(parent, r.storageClass)
	err := r.converge(ctx, parent)
	var diagnostic *diagnosticError
	if err != nil && !errors.As(err, &diagnostic) {
		return periodic, err
	}
	result := resultAnnotation{State: "Applied", InputHash: hash}
	if diagnostic != nil {
		result.State = "Error"
		result.Reason = diagnostic.reason
		result.Message = diagnostic.message
	}
	if err := r.report(ctx, parent, result); err != nil {
		return periodic, err
	}
	return periodic, nil
}

func (r *reconciler) converge(ctx context.Context, parent *unstructured.Unstructured) error {
	pvcName, serviceName, err := childNames(parent.GetUID())
	if err != nil {
		return err
	}
	previous, err := readState(parent)
	if err != nil {
		return err
	}
	desired, path, err := r.programs.plan(ctx, parent)
	if err != nil {
		return err
	}
	// All validation runs before the first durable write.
	next := previous
	scratch := parent.DeepCopy()
	if err := editTemplate(scratch, path, previous, desired, &next); err != nil {
		return err
	}
	if desired.Storage != nil && previous.Storage != nil && (desired.Storage.Size != previous.Storage.Size || r.storageClass != previous.Storage.StorageClass) {
		return failure("StorageChangeUnsupported", "storage capacity and class cannot change after reservation")
	}
	if desired.Service != nil || previous.Service {
		service := &corev1.Service{}
		err := r.reader.Get(ctx, types.NamespacedName{Namespace: parent.GetNamespace(), Name: serviceName}, service)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			if err := validateService(parent, service); err != nil {
				return err
			}
		}
	}
	if desired.Storage != nil || (previous.Storage != nil && previous.Storage.UID == "") {
		pvc := &corev1.PersistentVolumeClaim{}
		err := r.reader.Get(ctx, types.NamespacedName{Namespace: parent.GetNamespace(), Name: pvcName}, pvc)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if previous.Storage == nil {
			if err == nil {
				return failure("OwnershipConflict", "claim name is occupied before storage reservation")
			}
			pending := &storageState{Size: desired.Storage.Size, StorageClass: r.storageClass}
			previous.Storage = pending
			if err := r.patchState(ctx, parent, previous); err != nil {
				return err
			}
			// Only this successful reservation response authorizes this single Create call.
			fresh := &unstructured.Unstructured{}
			fresh.SetGroupVersionKind(r.gvk)
			if err := r.reader.Get(ctx, client.ObjectKeyFromObject(parent), fresh); err != nil {
				return err
			}
			saved, err := readState(fresh)
			if err != nil {
				return err
			}
			if fresh.GetUID() != parent.GetUID() || fresh.GetDeletionTimestamp() != nil || !reflect.DeepEqual(saved, previous) || fresh.GetAnnotations()[annotationStorage] != parent.GetAnnotations()[annotationStorage] {
				return failure("ParentChanged", "workload changed after storage reservation")
			}
			err = r.client.Create(ctx, desiredPVC(parent, pvcName, pending))
			if err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			pvc = &corev1.PersistentVolumeClaim{}
			if err := r.reader.Get(ctx, types.NamespacedName{Namespace: parent.GetNamespace(), Name: pvcName}, pvc); err != nil {
				return err
			}
		} else if apierrors.IsNotFound(err) {
			if desired.Storage != nil {
				if previous.Storage.UID == "" {
					return failure("StorageCreationUncertain", "reserved claim is absent; creation cannot be retried safely")
				}
				return failure("StorageMissing", "committed claim is absent; replacement is not created")
			}
			pvc = nil
		}
		if pvc != nil {
			if err := verifyPVC(parent, pvc, previous.Storage); err != nil {
				return err
			}
			if previous.Storage.UID == "" {
				committed := *previous.Storage
				committed.UID = pvc.UID
				previous.Storage = &committed
				if err := r.patchState(ctx, parent, previous); err != nil {
					return err
				}
			}
		}
	}
	// Service deletion precedes release of its selector label.
	if desired.Service == nil && previous.Service {
		if err := r.reconcileService(ctx, parent, serviceName, nil); err != nil {
			return err
		}
	}
	next = previous
	changed := parent.DeepCopy()
	if err := editTemplate(changed, path, previous, desired, &next); err != nil {
		return err
	}
	if err := saveState(changed, next); err != nil {
		return err
	}
	if err := r.patchParent(ctx, parent, changed); err != nil {
		return err
	}
	if desired.Service != nil {
		return r.reconcileService(ctx, parent, serviceName, desired.Service)
	}
	return nil
}

func (r *reconciler) guardParent(ctx context.Context, parent *unstructured.Unstructured) error {
	fresh := &unstructured.Unstructured{}
	fresh.SetGroupVersionKind(r.gvk)
	if err := r.reader.Get(ctx, client.ObjectKeyFromObject(parent), fresh); err != nil {
		return err
	}
	if fresh.GetUID() != parent.GetUID() || fresh.GetDeletionTimestamp() != nil || fresh.GetResourceVersion() != parent.GetResourceVersion() {
		return apierrors.NewConflict(schema.GroupResource{Group: r.gvk.Group, Resource: r.gvk.Kind}, parent.GetName(), errors.New("workload changed before child effect"))
	}
	return nil
}

func (r *reconciler) patchState(ctx context.Context, parent *unstructured.Unstructured, value state) error {
	changed := parent.DeepCopy()
	if err := saveState(changed, value); err != nil {
		return err
	}
	return r.patchParent(ctx, parent, changed)
}

func (r *reconciler) patchParent(ctx context.Context, parent, changed *unstructured.Unstructured) error {
	if reflect.DeepEqual(parent.Object, changed.Object) {
		return nil
	}
	if err := r.client.Patch(ctx, changed, client.MergeFromWithOptions(parent.DeepCopy(), client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	*parent = *changed
	return nil
}

func (r *reconciler) report(ctx context.Context, parent *unstructured.Unstructured, result resultAnnotation) error {
	if len(result.Message) > 384 {
		result.Message = result.Message[:384]
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(encoded) > 1024 {
		return errors.New("diagnostic exceeds 1024 bytes")
	}
	annotations := parent.GetAnnotations()
	if annotations[annotationResult] == string(encoded) {
		return nil
	}
	changed := parent.DeepCopy()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[annotationResult] = string(encoded)
	changed.SetAnnotations(annotations)
	if err := r.patchParent(ctx, parent, changed); err != nil {
		return err
	}
	if r.recorder != nil {
		eventType, reason, message := corev1.EventTypeNormal, "Applied", "plugin API configuration converged"
		if result.State == "Error" {
			eventType, reason, message = corev1.EventTypeWarning, result.Reason, result.Message
		}
		r.recorder.Eventf(parent, nil, eventType, reason, "Reconcile", "%s", message)
	}
	return nil
}
