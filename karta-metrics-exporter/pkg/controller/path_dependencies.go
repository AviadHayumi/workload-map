// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"slices"

	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

type pathDependent struct {
	owner types.UID
	pod   discovery.ObjectRef
}

// Runtime owns this index. Leaf and root changes already have direct generation
// guards; only intermediate inputs need reverse lookup to reach affected Pods.
func (r *Runtime) setPodPathLocked(root *rootState, pod types.UID, path []discovery.ObjectRef) {
	r.removePodPathLocked(root, pod)
	root.paths[pod] = slices.Clone(path)
	for i := 1; i+1 < len(path); i++ {
		key := keyOf(path[i])
		if r.pathDependents[key] == nil {
			r.pathDependents[key] = map[types.UID]pathDependent{}
		}
		r.pathDependents[key][pod] = pathDependent{owner: path[i].UID, pod: path[0]}
	}
}

func (r *Runtime) removePodPathLocked(root *rootState, pod types.UID) {
	path := root.paths[pod]
	for i := 1; i+1 < len(path); i++ {
		key := keyOf(path[i])
		delete(r.pathDependents[key], pod)
		if len(r.pathDependents[key]) == 0 {
			delete(r.pathDependents, key)
		}
	}
	delete(root.paths, pod)
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
