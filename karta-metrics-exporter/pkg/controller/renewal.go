// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"hash/fnv"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type pathDependent struct {
	owner    types.UID
	pod      discovery.ObjectRef
	position int
}

type metadataPathFailure struct {
	reason   string
	position int
	pod      discovery.ObjectRef
}

// Runtime and Reader are locked. Only already-valid observations may wait;
// mutations use Add and overtake delayed renewal on the same serialized key.
func (r *Runtime) scheduleRenewalLocked(uid types.UID) bool {
	state := r.roots[uid]
	if state == nil {
		return false
	}
	record := r.deps.Publications.Published(uid)
	healthy := record != nil && record.Workload.OwnerResolution != store.UnknownOwner &&
		len(record.FailureReasons) == 0 && len(state.dirty) == 0 &&
		len(state.resolutionErrors) == 0 &&
		state.publishedGeneration == state.generation && !state.fullRefresh &&
		r.definitionError == nil
	now := r.config.Now()
	earliest := time.Time{}
	check := func(proof store.Evidence) {
		if proof.Reason != "" && proof.Reason != store.ReasonNone || !now.Before(proof.ExpiresAt) {
			healthy = false
			return
		}
		if earliest.IsZero() || proof.ExpiresAt.Before(earliest) {
			earliest = proof.ExpiresAt
		}
		for _, key := range proof.Dependencies {
			if (strings.HasPrefix(key, "watch:") || strings.HasPrefix(key, "metadata:") || strings.HasPrefix(key, "ownership:")) && !r.reader.activeKeys[key] {
				healthy = false
			}
		}
	}
	if healthy {
		for _, pod := range state.pods {
			if pod.resolveError != nil {
				healthy = false
			}
		}
		check(record.Creation)
		if record.Workload.HasStatus {
			check(record.Status)
		}
		for _, page := range record.PodPages {
			for _, row := range page.Rows() {
				check(row.Mapping)
			}
		}
	}
	remaining := earliest.Sub(now)
	if !healthy || remaining <= 2*time.Second {
		state.renewalAt = time.Time{}
		state.fullRefresh = true
		r.rootQueue.Add(queueKey{Operation: "root", UID: uid})
		return false
	}
	window := min(r.config.RefreshInterval, remaining/2)
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(uid))
	delay := window/4 + time.Duration(hash.Sum64()%uint64(max(window*3/4, time.Nanosecond)))
	deadline := now.Add(delay)
	if state.renewalAt.IsZero() || deadline.Before(state.renewalAt) {
		state.renewalAt = deadline
	}
	r.rootQueue.AddAfter(queueKey{Operation: "root", UID: uid}, state.renewalAt.Sub(now))
	return true
}

// Retained paths still need recovery when expiry or pruning removed their last
// metadata entry. Roots without intermediate inputs have no work in this lane.
func (r *Runtime) refreshMetadata(ctx context.Context) error {
	r.reader.mu.RLock()
	hadInputs := len(r.reader.metadata) > 0 || len(r.reader.failures) > 0 || len(r.reader.metadataReads) > 0
	r.reader.mu.RUnlock()
	if hadInputs {
		r.pruneMetadata()
	}
	r.reader.expireMetadata()
	var err error
	if hadInputs {
		err = r.reader.refreshMetadata(ctx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !hadInputs && len(r.pathDependents) == 0 {
		return nil
	}
	r.reader.mu.RLock()
	r.scheduleMetadataPathsLocked()
	r.reader.mu.RUnlock()
	r.publishHealthLocked()
	return err
}

// Runtime and Reader are locked. Each edge is inspected once and each root is
// scheduled once, so a shared ancestor cannot cause repeated Pod-page scans.
func (r *Runtime) scheduleMetadataPathsLocked() {
	roots := map[types.UID]bool{}
	invalid := map[types.UID]metadataPathFailure{}
	failedSources := map[string]string{}
	for _, source := range r.watches {
		if source.err != nil {
			failedSources[source.failureKey] = failureReason(source.err)
		}
	}
	for key, dependents := range r.pathDependents {
		for pod, dependency := range dependents {
			root := r.roots[r.podRoots[pod]]
			if root == nil {
				continue
			}
			path := root.pods[pod].path
			position := dependency.position
			if position <= 0 || position+1 >= len(path) {
				continue
			}
			reason := r.metadataPathReasonLocked(key, path[position], path[position+1], failedSources)
			if reason == "" && root.pods[pod].resolveError != nil {
				reason = failureReason(root.pods[pod].resolveError)
			}
			if reason == "" && len(root.resolutionErrors) > 0 {
				reason = store.ReasonAPIUnavailable
			}
			uid := root.ref.UID
			roots[uid] = roots[uid] || reason != ""
			if previous, found := invalid[pod]; reason != "" && (!found || position < previous.position) {
				invalid[pod] = metadataPathFailure{reason: reason, position: position, pod: dependency.pod}
			}
		}
	}
	for pod, failure := range invalid {
		r.invalidatePodLocked(r.roots[r.podRoots[pod]], pod, failure.reason)
		r.queue.Add(queueKey{Operation: "object", Object: keyOf(failure.pod), UID: pod})
	}
	for uid, urgent := range roots {
		if urgent {
			r.rootQueue.Add(queueKey{Operation: "root", UID: uid})
		} else {
			r.scheduleRenewalLocked(uid)
		}
	}
}

func (r *Runtime) metadataPathReasonLocked(key objectKey, expected, parent discovery.ObjectRef, failedSources map[string]string) string {
	var object metav1.Object
	var proof store.Evidence
	if entry, watched := r.reader.objects[key]; watched {
		object, proof = entry.object, entry.evidence
	} else {
		if failure, failed := r.reader.failures[key]; failed && r.reader.options.Now().Before(failure.expires) {
			return failureReason(failure.cause)
		}
		if entry, found := r.reader.metadata[key]; found {
			object = entry.object
			proof = store.Evidence{ExpiresAt: entry.expires, Dependencies: []string{entry.failureKey}}
		}
	}
	if object == nil {
		return store.ReasonAPIUnavailable
	}
	owner, valid := controllerOwner(object)
	if object.GetUID() != expected.UID || !valid || owner.GVK != parent.GVK || owner.Name != parent.Name || owner.UID != parent.UID {
		return store.ReasonUnresolvedOwner
	}
	for _, key := range proof.Dependencies {
		if reason := failedSources[key]; reason != "" {
			return reason
		}
	}
	if object.GetResourceVersion() != expected.ResourceVersion || !r.config.Now().Before(proof.ExpiresAt) {
		return store.ReasonAPIUnavailable
	}
	if proof.Reason != "" && proof.Reason != store.ReasonNone {
		return proof.Reason
	}
	for _, key := range proof.Dependencies {
		if !r.reader.activeKeys[key] {
			return store.ReasonAPIUnavailable
		}
	}
	return ""
}

// Runtime owns this index. Leaf and root changes already have direct generation
// guards; only intermediate inputs need reverse lookup to reach affected Pods.
func (r *Runtime) setPodPathLocked(root *rootState, pod types.UID, path []discovery.ObjectRef) {
	r.removePodPathLocked(root, pod)
	entry := root.pods[pod]
	entry.path = slices.Clone(path)
	root.pods[pod] = entry
	for i := 1; i+1 < len(path); i++ {
		key := keyOf(path[i])
		if r.pathDependents[key] == nil {
			r.pathDependents[key] = map[types.UID]pathDependent{}
		}
		r.pathDependents[key][pod] = pathDependent{owner: path[i].UID, pod: path[0], position: i}
	}
}

func (r *Runtime) removePodPathLocked(root *rootState, pod types.UID) {
	path := root.pods[pod].path
	for i := 1; i+1 < len(path); i++ {
		key := keyOf(path[i])
		delete(r.pathDependents[key], pod)
		if len(r.pathDependents[key]) == 0 {
			delete(r.pathDependents, key)
		}
	}
	entry := root.pods[pod]
	entry.path = nil
	entry.resolveError = nil
	root.pods[pod] = entry
}

func (r *Runtime) invalidatePathDependentsLocked(ref discovery.ObjectRef, reason string) {
	for pod, dependency := range r.pathDependents[keyOf(ref)] {
		if dependency.owner != ref.UID {
			continue
		}
		if root := r.roots[r.podRoots[pod]]; root != nil {
			r.invalidatePodLocked(root, pod, reason)
			r.queue.Add(queueKey{Operation: "object", Object: keyOf(dependency.pod), UID: pod})
		}
	}
}
