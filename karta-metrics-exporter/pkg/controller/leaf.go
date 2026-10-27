// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"fmt"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Pods and selected objects share ownership discovery capacity. Root publication
// has its own workers so an unavailable owner cannot delay unrelated roots.
func (r *Runtime) resolveInput(ctx context.Context, key inputKey) error {
	if key.Object.GVK == podGVK {
		return r.resolveLeaf(ctx, key)
	}
	r.mu.Lock()
	candidate := r.candidates[key.UID][key.Object]
	if candidate == nil || !candidate.dirty {
		r.mu.Unlock()
		return nil
	}
	ref := candidate.ref
	r.mu.Unlock()
	return r.admitRoot(ctx, ref)
}

func (r *Runtime) enqueueObjectLocked(ref discovery.ObjectRef) {
	switch ref.GVK {
	case kartaGVK, namespaceGVK:
		return
	case podGVK:
	default:
		if r.candidates[ref.UID] == nil {
			r.candidates[ref.UID] = map[objectKey]*rootCandidate{}
		}
		candidate := r.candidates[ref.UID][keyOf(ref)]
		if candidate == nil {
			candidate = &rootCandidate{}
			r.candidates[ref.UID][keyOf(ref)] = candidate
		}
		candidate.ref, candidate.dirty = ref, true
	}
	r.inputQueue.Add(inputKey{Object: keyOf(ref), UID: ref.UID})
}

// A leaf changes attribution. Publication belongs to the root queue.
func (r *Runtime) resolveLeaf(ctx context.Context, key inputKey) error {
	ref := discovery.ObjectRef{GVK: podGVK, Namespace: key.Object.Namespace, Name: key.Object.Name, UID: key.UID}
	object, ok := r.reader.Cached(ref)
	if !ok {
		return nil
	}
	ref = refOf(object)
	if !r.podSelector.Matches(labelsFor(object)) {
		r.mu.Lock()
		if root := r.roots[r.podRoots[ref.UID]]; root != nil {
			r.invalidatePodLocked(root, ref.UID, store.ReasonUnresolvedOwner)
			r.removePodPathLocked(root, ref.UID)
			delete(r.podRoots, ref.UID)
		}
		r.mu.Unlock()
		return nil
	}
	workload, watch, err := r.discoverPath(ctx, ref)
	if err != nil || workload == nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	root, err := r.acceptPathLocked(ref, workload, watch)
	if err != nil {
		return err
	}
	if previous := r.podRoots[ref.UID]; previous != "" && previous != workload.Root.UID {
		if old := r.roots[previous]; old != nil {
			r.invalidatePodLocked(old, ref.UID, store.ReasonUnresolvedOwner)
			r.removePodPathLocked(old, ref.UID)
		}
	}
	if root.pods[ref.UID].resolveError != nil {
		r.invalidatePodLocked(root, ref.UID, store.ReasonAPIUnavailable)
	}
	pod := root.pods[ref.UID]
	pod.resolveError = nil
	if pod.proofKey == "" {
		r.sequence++
		pod.proofKey = fmt.Sprintf("pod:%s:%d", ref.UID, r.sequence)
	}
	root.pods[ref.UID] = pod
	r.setPodPathLocked(root, ref.UID, workload.Path)
	r.dirtyPod(root, ref.UID)
	r.podRoots[ref.UID] = workload.Root.UID
	return nil
}
