// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// CoverageError identifies a representation needed by resolution or evaluation.
type CoverageError struct {
	Request CoverageRequest
	Err     error
}

type coverageRequirement uint8

const (
	// Positive owner lookups can use accepted objects before the initial list finishes.
	acceptedCoverage coverageRequirement = iota
	synchronizedCoverage
	fullObjectCoverage
)

func (e *CoverageError) Error() string { return fmt.Sprintf("%s: %v", e.Request.GVK, e.Err) }

func (e *CoverageError) Unwrap() error { return e.Err }

func (d *Discovery) Resolve(ctx context.Context, ref ObjectRef) (GenericWorkload, error) {
	if err := ctx.Err(); err != nil {
		return GenericWorkload{}, err
	}
	d.mu.RLock()
	workload, missing, err := d.resolveLocked(ref, acceptedCoverage)
	d.mu.RUnlock()
	if missing != nil {
		err = d.missingObject(ctx, *missing, false)
	}
	if err != nil {
		return GenericWorkload{}, err
	}
	if d.inventory == nil {
		return workload, fmt.Errorf("%w: no definition inventory", ErrUnavailable)
	}
	selection, err := d.inventory.Lookup(workload.root.GVK)
	if err != nil {
		return workload, classifyError(err)
	}
	if !d.IsCurrent(workload.state) {
		return GenericWorkload{}, ErrStale
	}
	if selection.Definition != nil {
		if selection.GVK.GroupKind() != workload.root.GVK.GroupKind() || selection.GVK.Version == "" {
			return workload, fmt.Errorf("%w: selection targets a different root", ErrInvalidDefinition)
		}
		selection.Definition = selection.Definition.DeepCopy()
		d.mu.RLock()
		// Status depends on the root, not on the Pod that led to this handle.
		rootState := d.newStateLocked()
		node := d.ownership.nodes[workload.root.UID]
		if node != nil {
			d.captureLocked(&rootState, node, node.objects[workload.root.GVK])
		}
		rootState.definitionKind = workload.state.definitionKind
		rootState.definitionRevision = workload.state.definitionRevision
		d.mu.RUnlock()
		if !d.IsCurrent(workload.state, rootState) {
			return GenericWorkload{}, ErrStale
		}
		workload.karta = &KartaWorkload{root: workload.root, selection: selection, discovery: d, state: rootState}
	}
	return workload, nil
}

func (d *Discovery) resolveLocked(ref ObjectRef, requirement coverageRequirement) (GenericWorkload, *ObjectRef, error) {
	state := d.newStateLocked()
	workload := GenericWorkload{Resolution: UnknownOwner}
	seen := make(map[types.UID]struct{})
	current := ref
	for {
		if _, ok := seen[current.UID]; ok || current.UID == "" {
			return workload, nil, fmt.Errorf("%w: cyclic or missing UID", ErrUnresolvedOwner)
		}
		seen[current.UID] = struct{}{}
		node := d.ownership.nodes[current.UID]
		if node == nil {
			return workload, &current, ErrPending
		}
		object, ok := node.objects[current.GVK]
		if !ok {
			return workload, &current, ErrPending
		}
		if current.Name != object.Ref.Name || (current.Namespace != object.Ref.Namespace && object.Ref.Namespace != "") {
			return workload, nil, fmt.Errorf("%w: owner identity mismatch", ErrUnresolvedOwner)
		}
		if current.ResourceVersion != "" && current.ResourceVersion != object.Ref.ResourceVersion {
			return workload, nil, ErrStale
		}
		if err := d.coverageErrorLocked(object, requirement); err != nil {
			return workload, nil, err
		}
		d.captureLocked(&state, node, object)
		workload.Path = append(workload.Path, object.Ref)
		resolution, parent, err := d.parentLocked(node, object.Ref)
		if err != nil {
			return workload, nil, err
		}
		if parent == nil {
			workload.root = object.Ref
			workload.Resolution = resolution
			state.definitionKind = object.Ref.GVK.GroupKind()
			state.definitionRevision = d.definitions[state.definitionKind]
			workload.state = state
			return workload, nil, nil
		}
		current = ObjectRef{GVK: parent.GVK, Namespace: parent.Namespace, Name: parent.Name, UID: parent.UID}
	}
}

func (d *Discovery) parentLocked(node *OwnerNode, ref ObjectRef) (OwnerResolution, *OwnerKey, error) {
	var parent *OwnerKey
	first := true
	for _, object := range node.objects {
		next, err := controllerOwner(object.Owners, object.Ref.Namespace)
		if err != nil {
			return UnknownOwner, nil, fmt.Errorf("%w: invalid owner of %s", err, ref.Name)
		}
		if !first && ((next == nil) != (parent == nil) || next != nil && *next != *parent) {
			return UnknownOwner, nil, fmt.Errorf("%w: API representations disagree on owner", ErrStale)
		}
		parent, first = next, false
	}
	if parent == nil {
		return Resolved, nil, nil
	}
	if _, ignored := d.ownership.ignoredOwners[parent.GVK]; ignored {
		return IgnoredOwner, nil, nil
	}
	return Resolved, parent, nil
}

func (d *Discovery) newStateLocked() CapturedState {
	return CapturedState{discovery: d, revision: d.revision}
}

func (d *Discovery) captureLocked(state *CapturedState, node *OwnerNode, object observation) {
	state.objects = append(state.objects, objectVersion{
		ref: object.Ref, key: object.WatchKey, run: object.Run, revision: object.revision,
		edgeRevision: node.edgeRevision, coverageRevision: d.coverage[object.WatchKey].revision,
	})
}

// IsCurrent checks only locally accepted discovery inputs. Call it in the same
// publication critical section as controller-owned evidence checks and commit.
func (d *Discovery) IsCurrent(states ...CapturedState) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(states) == 0 {
		return false
	}
	for _, state := range states {
		if !d.isCurrentLocked(state) {
			return false
		}
	}
	return true
}

func (d *Discovery) isCurrentLocked(state CapturedState) bool {
	if state.discovery != d || len(state.objects) == 0 {
		return false
	}
	// Every accepted input change advances the discovery revision.
	if state.revision == d.revision {
		return true
	}
	for _, version := range state.objects {
		node := d.ownership.nodes[version.ref.UID]
		if node == nil {
			return false
		}
		object, ok := node.objects[version.ref.GVK]
		if !ok || object.Ref != version.ref || object.revision != version.revision || object.WatchKey != version.key || object.Run != version.run {
			return false
		}
		if node.edgeRevision != version.edgeRevision || d.coverage[version.key].revision != version.coverageRevision {
			return false
		}
	}
	if state.membershipUID != "" {
		if node := d.ownership.nodes[state.membershipUID]; node == nil || node.membershipRevision != state.membershipRevision {
			return false
		}
	}
	if d.definitions[state.definitionKind] != state.definitionRevision {
		return false
	}
	return true
}

func (d *Discovery) coverageErrorLocked(object observation, requirement coverageRequirement) error {
	full := requirement == fullObjectCoverage
	c, ok := d.coverage[object.WatchKey]
	var err error
	switch {
	case !ok || c.Removed:
		err = ErrPending
	case c.Err != nil:
		err = classifyError(c.Err)
	case c.Run != object.Run,
		requirement != acceptedCoverage && !c.Ready,
		full && !c.FullObject:
		err = ErrPending
	}
	if err != nil {
		return &CoverageError{Request: CoverageRequest{GVK: object.Ref.GVK, Namespace: object.Ref.Namespace, FullObject: full}, Err: err}
	}
	return nil
}

func (d *Discovery) missingObject(ctx context.Context, ref ObjectRef, full bool) error {
	request := CoverageRequest{GVK: ref.GVK, Namespace: ref.Namespace, FullObject: full}
	if d.reader == nil {
		return &CoverageError{Request: request, Err: ErrPending}
	}
	object, err := d.reader.Get(ctx, ref)
	if err != nil {
		return &CoverageError{Request: request, Err: classifyError(err)}
	}
	if object == nil || object.GetUID() != ref.UID || object.GetName() != ref.Name {
		return fmt.Errorf("%w: cached owner does not match reference", ErrUnresolvedOwner)
	}
	// The informer can advance before its handler. Never adopt unaccepted facts.
	return &CoverageError{Request: request, Err: ErrStale}
}

func (d *Discovery) readObject(ctx context.Context, object observation) (*unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.reader == nil {
		return nil, ErrUnavailable
	}
	body, err := d.reader.Get(ctx, object.Ref)
	if err != nil {
		return nil, classifyError(err)
	}
	ref := object.Ref
	if body == nil || body.GetUID() != ref.UID || body.GetResourceVersion() != ref.ResourceVersion ||
		body.GetName() != ref.Name || body.GetNamespace() != ref.Namespace || body.GroupVersionKind() != ref.GVK {
		return nil, ErrStale
	}
	return body.DeepCopy(), nil
}

func classifyError(err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded, ErrPending, ErrPermissionDenied, ErrUnavailable, ErrUnresolvedOwner, ErrInvalidDefinition, ErrEvaluation, ErrStale} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return fmt.Errorf("%w: %w", ErrPermissionDenied, err)
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%w: %w", ErrUnresolvedOwner, err)
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}

// Walk visits the observed descendant subtree, including intermediate Pods.
// Visitors run outside discovery's lock. The token does not prove completeness.
func (w GenericWorkload) Walk(ctx context.Context, visit func(ObjectRef, *ObjectRef) error) (CapturedState, error) {
	if err := ctx.Err(); err != nil {
		return CapturedState{}, err
	}
	d := w.state.discovery
	if d == nil {
		return CapturedState{}, ErrStale
	}
	type entry struct {
		ref    ObjectRef
		parent *ObjectRef
	}
	d.mu.RLock()
	if !d.isCurrentLocked(w.state) {
		d.mu.RUnlock()
		return CapturedState{}, ErrStale
	}
	state := cloneState(w.state)
	state.membershipUID = w.root.UID
	state.membershipRevision = d.ownership.nodes[w.root.UID].membershipRevision
	pending := []entry{{ref: w.root}}
	var entries []entry
	seen := make(map[types.UID]struct{})
	for len(pending) > 0 {
		next := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, visited := seen[next.ref.UID]; visited {
			d.mu.RUnlock()
			return CapturedState{}, ErrUnresolvedOwner
		}
		seen[next.ref.UID] = struct{}{}
		node := d.ownership.nodes[next.ref.UID]
		object := node.objects[next.ref.GVK]
		if _, _, err := d.parentLocked(node, next.ref); err != nil {
			d.mu.RUnlock()
			return CapturedState{}, err
		}
		if err := d.coverageErrorLocked(object, synchronizedCoverage); err != nil {
			d.mu.RUnlock()
			return CapturedState{}, err
		}
		d.captureLocked(&state, node, object)
		entries = append(entries, next)
		childUIDs := slices.Sorted(maps.Keys(d.ownership.children[next.ref.UID]))
		for i := len(childUIDs) - 1; i >= 0; i-- {
			child := d.ownership.nodes[childUIDs[i]]
			if child == nil {
				continue
			}
			refs := make([]ObjectRef, 0, len(child.objects))
			for _, observed := range child.objects {
				refs = append(refs, observed.Ref)
			}
			slices.SortFunc(refs, compareRefs)
			selected := refs[0]
			for _, ref := range refs {
				if d.coverageErrorLocked(child.objects[ref.GVK], synchronizedCoverage) == nil {
					selected = ref
					break
				}
			}
			parent := next.ref
			pending = append(pending, entry{ref: selected, parent: &parent})
		}
	}
	d.mu.RUnlock()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return CapturedState{}, err
		}
		if err := visit(entry.ref, entry.parent); err != nil {
			return CapturedState{}, err
		}
	}
	if !d.IsCurrent(state) {
		return CapturedState{}, ErrStale
	}
	return state, nil
}

func cloneState(state CapturedState) CapturedState {
	state.objects = slices.Clone(state.objects)
	return state
}
