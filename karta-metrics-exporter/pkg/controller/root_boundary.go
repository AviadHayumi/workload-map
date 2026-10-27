// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type versionCounts map[schema.GroupKind]map[schema.GroupVersionKind]int

// Counts cover watch scopes and metadata entries, not a second object cache.
func trackVersion(index *versionCounts, gvk schema.GroupVersionKind, delta int) {
	if *index == nil {
		*index = versionCounts{}
	}
	versions := (*index)[gvk.GroupKind()]
	if versions == nil {
		versions = map[schema.GroupVersionKind]int{}
		(*index)[gvk.GroupKind()] = versions
	}
	versions[gvk] += delta
	if versions[gvk] <= 0 {
		delete(versions, gvk)
	}
	if len(versions) == 0 {
		delete(*index, gvk.GroupKind())
	}
}

// Caller holds Reader's lock after Runtime's guard.
func (r *Runtime) trackWatchVersion(gvk schema.GroupVersionKind, delta int) {
	trackVersion(&r.reader.versions, gvk, delta)
}

func (r *Runtime) terminalOwnerBoundary(object metav1.Object) bool {
	return terminalOwnerBoundary(object, r.config.IgnoredOwnerKinds)
}

func (r *Reader) terminalOwnerBoundary(object metav1.Object) bool {
	return terminalOwnerBoundary(object, r.ignoredOwners)
}

func terminalOwnerBoundary(object metav1.Object, ignored []schema.GroupVersionKind) bool {
	owner, valid := controllerOwner(object)
	return valid && (owner.UID == "" || slices.Contains(ignored, owner.GVK))
}

func controllerOwner(object metav1.Object) (discovery.ObjectRef, bool) {
	return discovery.ControllerOwner(object)
}

// A newer object version supersedes verification, but does not by itself mean
// its controller owner disappeared. Non-controller references are not traversed.
func objectChangeReason(previous, current metav1.Object) string {
	before, beforeValid := controllerOwner(previous)
	after, afterValid := controllerOwner(current)
	if previous.GetUID() != current.GetUID() || !beforeValid || !afterValid || before != after {
		return store.ReasonUnresolvedOwner
	}
	return store.ReasonAPIUnavailable
}

// Runtime and Reader are locked by the caller. A publication's removal must not
// erase accepted evidence that its former root still has a controller owner.
func (r *Runtime) rootBoundaryLocked(ref discovery.ObjectRef) bool {
	for gvk := range r.reader.versions[ref.GVK.GroupKind()] {
		key := objectKey{GVK: gvk, Namespace: ref.Namespace, Name: ref.Name}
		if entry, found := r.reader.objects[key]; found {
			if entry.object.GetUID() == ref.UID && !r.terminalOwnerBoundary(entry.object) {
				return false
			}
			continue
		}
		// Expired contradictions remain barriers until retirement also withdraws alias proof.
		if entry, found := r.reader.metadata[key]; found && entry.object.GetUID() == ref.UID && !r.terminalOwnerBoundary(entry.object) {
			return false
		}
	}
	return true
}
