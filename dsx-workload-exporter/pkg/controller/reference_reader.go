// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/dsx-workload-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

// One SDK factory reads references for one observation of a workload. Its
// watches outlive that observation so target changes can enqueue the parent.
type referenceReader struct {
	state      *controllerState
	root       discovery.ObjectRef
	selection  selectedDefinition
	sources    []watchKey
	observed   *rootState
	generation uint64
}

func (r *referenceReader) Get(ctx context.Context, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	if name == "" {
		return nil, errors.New("reference lookup requires a name")
	}
	objects, err := r.read(ctx, gvk, namespace, name, nil)
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, references.ErrNotFound
	}
	return &objects[0], nil
}

func (r *referenceReader) List(ctx context.Context, gvk schema.GroupVersionKind, q references.ListQuery) ([]unstructured.Unstructured, error) {
	return r.read(ctx, gvk, q.Namespace, "", q.Selector)
}

func (r *referenceReader) read(ctx context.Context, gvk schema.GroupVersionKind, namespace, name string, selector labels.Selector) ([]unstructured.Unstructured, error) {
	s := r.state
	s.stateMu.RLock()
	if r.observed == nil {
		r.observed = s.roots[r.root.UID]
		if r.observed != nil {
			r.generation = r.observed.referenceGeneration
		}
	}
	s.stateMu.RUnlock()
	mapping, err := s.reader.restMapping(ctx, gvk)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// No collection exists to notify this job when discovery recovers.
		s.workloadQueue.AddAfter(workloadJobKey{RootUID: r.root.UID}, mapperResetInterval)
		s.stateMu.RLock()
		if root := s.roots[r.root.UID]; root != nil {
			for _, pod := range root.pods {
				s.podQueue.AddAfter(pod, mapperResetInterval)
			}
		}
		s.stateMu.RUnlock()
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, discovery.ErrUnavailable
		}
		return nil, err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		namespace = ""
	} else if namespace == "" || namespace != r.root.Namespace {
		return nil, errors.New("namespaced reference requires the workload namespace")
	}

	key, err := s.ensureMappedWatch(ctx, mapping, gvk, namespace, false, r)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(r.sources, key) {
		r.sources = append(r.sources, key)
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := s.watches[key]
	if replacement := s.replacements[key]; replacement != nil {
		w = replacement
	}
	if w == nil {
		return nil, discovery.ErrPending
	}
	// A missing collection or a failed LIST/WATCH is not a missing lookup.
	if w.err != nil {
		if apierrors.IsNotFound(w.err) {
			return nil, discovery.ErrUnavailable
		}
		return nil, w.err
	}
	if !w.ready || !w.full {
		return nil, discovery.ErrPending
	}
	if name != "" {
		entry := s.objects[objectKey{GVK: gvk, Namespace: namespace, Name: name}]
		if entry.object != nil && entry.source == key {
			return []unstructured.Unstructured{*entry.object.DeepCopy()}, nil
		}
		return nil, nil
	}
	var items []unstructured.Unstructured
	for k := range w.objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if k.Namespace != namespace {
			continue
		}
		object := s.objects[k].object
		if selector == nil || selector.Matches(labelsFor(object)) {
			items = append(items, *object.DeepCopy())
		}
	}
	return items, nil
}

// Called under stateMu before publishing either a successful or a failed read.
func (r *referenceReader) isCurrent() bool {
	return r.observed == nil || (r.state.roots[r.root.UID] == r.observed && r.observed.referenceGeneration == r.generation)
}

func (w *resourceWatch) trackReferenceReader(uid types.UID, namespace string) {
	if w.dependents == nil {
		w.dependents = make(map[types.UID]string)
	}
	w.dependents[uid] = namespace
}

// Callers hold stateMu. An empty event namespace includes cluster references
// and collection-wide changes such as initial synchronization or watch failure.
func (r *controllerState) requeueReferenceReaders(w *resourceWatch, eventNamespace string) {
	for uid, namespace := range w.dependents {
		if eventNamespace == "" || namespace == eventNamespace {
			if root := r.roots[uid]; root != nil {
				root.referenceGeneration++
				r.enqueueRootAndMembers(uid)
			}
		}
	}
}

func (r *controllerState) releaseReferenceWatches(uid types.UID) {
	for _, w := range r.watches {
		delete(w.dependents, uid)
		if w.referenceOnly && len(w.dependents) == 0 {
			r.retireWatch(w)
		}
	}
}
