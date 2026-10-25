// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"hash/fnv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Runtime and Reader are locked. Only already-valid observations may wait;
// mutations use Add and overtake delayed renewal on the same serialized key.
func (r *Runtime) scheduleRenewalLocked(uid types.UID) bool {
	state := r.roots[uid]
	if state == nil {
		return false
	}
	record := r.deps.Publications.Published(uid)
	healthy := record != nil && record.Workload.OwnerResolution != store.UnknownOwner &&
		len(record.FailureReasons) == 0 && len(state.dirty) == 0 && len(state.podErrors) == 0 &&
		len(state.labelFailures) == 0 && len(state.resolutionErrors) == 0 &&
		state.publishedGeneration == state.generation && !state.fullRefresh &&
		r.definitionError == nil && !r.pendingDefinitions[state.ref.GVK.GroupKind()]
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
		check(record.Creation)
		if record.Workload.HasStatus {
			check(record.Status)
		}
		for _, page := range record.PodPages {
			for _, row := range page.Rows {
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
	if !state.renewalAt.IsZero() {
		return true
	}
	window := min(r.config.RefreshInterval, remaining/2)
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(uid))
	delay := window/4 + time.Duration(hash.Sum64()%uint64(max(window*3/4, time.Nanosecond)))
	state.renewalAt = now.Add(delay)
	r.rootQueue.AddAfter(queueKey{Operation: "root", UID: uid}, delay)
	return true
}

// A source with no cached, failed or pending owner inputs has no graph work.
// Capture this before expiry/pruning so removed dependencies still re-resolve.
func (r *Runtime) refreshMetadata(ctx context.Context) error {
	r.reader.mu.RLock()
	hadInputs := len(r.reader.metadata) > 0 || len(r.reader.failures) > 0 || len(r.reader.pending) > 0 || len(r.reader.priming) > 0
	r.reader.mu.RUnlock()
	if hadInputs {
		r.pruneMetadata()
	}
	r.reader.expireMetadata()
	if !hadInputs {
		return nil
	}
	err := r.reader.refreshMetadata(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	for uid, root := range r.roots {
		for pod, path := range root.paths {
			if len(path) > 0 {
				r.queue.Add(queueKey{Operation: "object", Object: keyOf(path[0]), UID: pod})
			}
		}
		root.fullRefresh = true
		r.rootQueue.Add(queueKey{Operation: "root", UID: uid})
	}
	r.publishHealthLocked()
	return err
}
