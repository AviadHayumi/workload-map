// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

// PodReconciler demonstrates the consumer-owned boundary around discovery.
// A Pod event enqueues a namespace/name. The worker reads the current Pod rather
// than using a possibly stale event object. The caller owns watches, cache
// readiness, retries, and serialization of reconciles for the same key.
type PodReconciler struct {
	Discoverer karta.Discoverer
	GetPod     func(context.Context, types.NamespacedName) (*corev1.Pod, error)

	// Save replaces the consumer's Pod-to-workload association. If the root
	// changed, the consumer also schedules the previous root for refresh.
	// With concurrent Pod workers, queue root-key reconciliation and serialize
	// fresh acquisition plus publication by root. Serializing writes alone can
	// publish an older observation after a newer one.
	Save func(context.Context, types.NamespacedName, *discovery.Workload) error

	// Remove removes the saved association and schedules its previous root
	// for refresh. Removing one Pod must not remove a workload with other Pods.
	Remove func(context.Context, types.NamespacedName) error
}

// Reconcile discovers a present Pod or removes the association for an absent
// Pod. Read and discovery errors leave the saved projection unchanged.
func (r *PodReconciler) Reconcile(ctx context.Context, key types.NamespacedName) error {
	if r.Discoverer == nil || r.GetPod == nil || r.Save == nil || r.Remove == nil {
		return fmt.Errorf("pod reconciler requires Discoverer, GetPod, Save, and Remove")
	}
	pod, err := r.GetPod(ctx, key)
	if apierrors.IsNotFound(err) {
		if err := r.Remove(ctx, key); err != nil {
			return fmt.Errorf("remove Pod association %s: %w", key, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Pod %s: %w", key, err)
	}
	if pod == nil || pod.Namespace != key.Namespace || pod.Name != key.Name {
		return fmt.Errorf("pod reader returned a different object for %s", key)
	}
	observed, err := r.Discoverer.WorkloadForPod(ctx, discovery.PodReference(pod))
	if err != nil {
		return fmt.Errorf("discover workload for Pod %s: %w", key, err)
	}
	if err := r.Save(ctx, key, observed); err != nil {
		return fmt.Errorf("save Pod association %s: %w", key, err)
	}
	return nil
}
