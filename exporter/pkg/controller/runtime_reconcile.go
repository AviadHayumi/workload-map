// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

type evaluatedPod struct {
	ref      discovery.ObjectRef
	root     discovery.ObjectRef
	path     []discovery.ObjectRef
	mapping  discovery.PodMapping
	pod      *corev1.Pod
	err      error
	ownerErr bool
	visible  bool
}

func (c *Runtime) reconcileObject(ctx context.Context, key queuedWork) error {
	c.mu.Lock()
	fact, exists := c.reader.objects[objectKey{GVK: key.GVK, Namespace: key.Namespace, Name: key.Name}]
	c.mu.Unlock()
	if !exists || fact.observation.Ref.UID != key.UID {
		return nil
	}
	workload, err := c.discovery.Resolve(ctx, fact.observation.Ref)
	if err != nil {
		if root := workload.Root(); root.UID != "" {
			c.queue.Add(queuedWork{Operation: "root", UID: root.UID})
		}
		if !errors.Is(err, discovery.ErrPending) && !errors.Is(err, discovery.ErrStale) {
			resolution := discovery.UnknownOwner
			if workload.Root().UID != "" {
				resolution = workload.Resolution
			}
			c.retainPodFailure(fact.observation.Ref, err, resolution)
		}
		return err
	}
	root := workload.Root()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.discovery.IsCurrent(workload.State()) {
		return discovery.ErrStale
	}
	if fact.observation.Ref.GVK == runtimePodGVK {
		if old := c.associations[key.UID]; old != "" && old != root.UID {
			c.unlinkPodLocked(key.UID)
			if record := c.store.Published(old); record != nil {
				c.store.Publish(record.WithPod(key.UID, nil))
			}
			if history, ok := c.history[old]; ok {
				history.record = history.record.WithPod(key.UID, nil)
				c.history[old] = history
			}
			c.invalidateMembershipLocked(root.Namespace)
			c.queue.Add(queuedWork{Operation: "root", UID: old})
		}
		c.associations[key.UID] = root.UID
		if c.podsByRoot[root.UID] == nil {
			c.podsByRoot[root.UID] = map[types.UID]struct{}{}
		}
		c.podsByRoot[root.UID][key.UID] = struct{}{}
	}
	c.queue.Add(queuedWork{Operation: "root", UID: root.UID})
	return nil
}

func (c *Runtime) reconcileRoot(ctx context.Context, uid types.UID) error {
	c.mu.Lock()
	refs := make([]discovery.ObjectRef, 0, 2)
	for key := range c.reader.byUID[uid] {
		refs = append(refs, c.reader.objects[key].observation.Ref)
	}
	if len(refs) == 0 {
		c.mu.Unlock()
		return nil
	}
	// A stable representation avoids watch-arrival order choosing generic labels.
	sort.Slice(refs, func(i, j int) bool { return refs[i].GVK.String() < refs[j].GVK.String() })
	rootRef := refs[0]
	expected := c.selections[rootRef.GVK.GroupKind()]
	if expected.Definition != nil {
		rootRef.GVK = expected.GVK
		fact, exists := c.reader.objects[refKey(rootRef)]
		if !exists || fact.observation.Ref.UID != uid {
			c.queue.Add(queuedWork{Operation: "coverage", GVK: expected.GVK, Namespace: rootRef.Namespace, Full: true})
			c.mu.Unlock()
			return discovery.ErrPending
		}
		rootRef = fact.observation.Ref
	}
	scopeRevision, definitionRevision := c.scopeRevision, c.definitionRevision
	if rootRef.Namespace == "" || !c.namespaces[rootRef.Namespace] {
		c.removeRootLocked(uid, rootRef.Namespace)
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	actual, err := c.inventory.Lookup(rootRef.GVK)
	if err == nil && !sameSelection(actual, expected) {
		return discovery.ErrStale
	}
	workload, err := c.discovery.Resolve(ctx, rootRef)
	if err != nil {
		if workload.Root().UID == uid && !errors.Is(err, discovery.ErrPending) && !errors.Is(err, discovery.ErrStale) {
			c.publishSelectionFailure(ctx, workload, rootRef, err)
			return err
		}
		if !errors.Is(err, discovery.ErrPending) && !errors.Is(err, discovery.ErrStale) {
			c.retainRootFailure(uid, err)
		}
		return err
	}
	if workload.Root().UID != uid {
		c.mu.Lock()
		c.removeRootLocked(uid, rootRef.Namespace)
		c.mu.Unlock()
		return nil
	}
	root, err := c.reader.Get(ctx, rootRef)
	if err != nil {
		return err
	}
	interpretation := workload.KartaWorkload()
	allowedRoots := map[schema.GroupVersionKind]bool{}
	if interpretation == nil {
		for _, ref := range refs {
			body, readErr := c.reader.Get(ctx, ref)
			if readErr != nil {
				return readErr
			}
			allowedRoots[ref.GVK], err = c.rootSelected(ref, body)
			if err != nil {
				return err
			}
			if !allowedRoots[rootRef.GVK] && allowedRoots[ref.GVK] {
				rootRef, root = ref, body
			}
		}
		if rootRef != workload.Root() {
			workload, err = c.discovery.Resolve(ctx, rootRef)
			if err != nil || workload.KartaWorkload() != nil {
				return discovery.ErrStale
			}
		}
	} else {
		allowedRoots[rootRef.GVK], err = c.rootSelected(rootRef, root)
		if err != nil {
			return err
		}
	}
	if !allowedRoots[rootRef.GVK] || (rootRef.GVK == runtimePodGVK && interpretation == nil) {
		c.mu.Lock()
		if c.discovery.IsCurrent(workload.State()) {
			c.removeRootLocked(uid, rootRef.Namespace)
		}
		c.mu.Unlock()
		return nil
	}
	podRefs := map[types.UID]discovery.ObjectRef{}
	walkState, walkErr := workload.Walk(ctx, func(ref discovery.ObjectRef, _ *discovery.ObjectRef) error {
		if ref.GVK == runtimePodGVK {
			podRefs[ref.UID] = ref
		}
		return nil
	})
	if walkErr != nil {
		c.enqueueCoverageError(walkErr)
	}
	// A successful walk omits children detached by a deleted intermediate
	// owner. Resolve prior associations too, retaining only applicable history.
	c.mu.Lock()
	for podUID := range c.podsByRoot[uid] {
		if _, walked := podRefs[podUID]; walked {
			continue
		}
		for key := range c.reader.byUID[podUID] {
			if key.GVK == runtimePodGVK {
				podRefs[podUID] = c.reader.objects[key].observation.Ref
			}
		}
	}
	c.mu.Unlock()
	batch := c.discovery.NewEvaluationBatch()
	var status discovery.StatusResult
	var statusErr error
	if interpretation != nil && !sameSelection(interpretation.Selection(), expected) {
		return discovery.ErrStale
	}
	if interpretation != nil {
		status, statusErr = interpretation.Status(ctx, batch)
	}
	evaluated := make([]evaluatedPod, 0, len(podRefs))
	tokens := []discovery.CapturedState{workload.State()}
	if walkErr == nil {
		tokens = append(tokens, walkState)
	}
	if interpretation != nil && statusErr == nil {
		tokens = append(tokens, status.State())
	}
	for _, ref := range podRefs {
		item := evaluatedPod{ref: ref}
		body, readErr := c.reader.Get(ctx, ref)
		if readErr != nil {
			c.enqueueCoverageError(readErr)
			item.err, item.ownerErr, item.visible = readErr, true, true
			evaluated = append(evaluated, item)
			continue
		}
		pod := &corev1.Pod{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(body.Object, pod); err != nil {
			item.err, item.visible = err, true
			evaluated = append(evaluated, item)
			continue
		}
		item.pod = pod
		item.visible = c.podSelector.Matches(labels.Set(pod.Labels))
		resolved, resolveErr := c.discovery.Resolve(ctx, ref)
		if resolved.Root().UID != "" && resolved.Root().UID != uid {
			continue
		}
		if resolveErr != nil {
			c.enqueueCoverageError(resolveErr)
			item.err, item.ownerErr = resolveErr, resolved.Root().UID == ""
			evaluated = append(evaluated, item)
			continue
		}
		item.root = resolved.Root()
		if interpretation == nil {
			item.visible = item.visible && allowedRoots[item.root.GVK]
		}
		item.path = resolved.Path
		tokens = append(tokens, resolved.State())
		if interpretation != nil {
			item.mapping, item.err = interpretation.PodMapping(ctx, pod, batch)
			c.enqueueCoverageError(item.err)
			if item.err == nil {
				tokens = append(tokens, item.mapping.State())
			}
		}
		evaluated = append(evaluated, item)
	}
	submittedBy := ""
	submitterOK := walkErr == nil
	if c.submitter != nil {
		identity, extractErr := c.submitter.Evaluate(ctx, root, rootRef.GVK)
		if extractErr != nil {
			submitterOK = false
		} else {
			submittedBy = identity.Identity
		}
		for _, item := range evaluated {
			if item.pod == nil || item.ownerErr {
				submitterOK = false
				continue
			}
			identity, extractErr := c.submitter.Evaluate(ctx, item.pod, runtimePodGVK)
			if extractErr != nil {
				submitterOK = false
			} else if identity.Identity != "" {
				if submittedBy == "" {
					submittedBy = identity.Identity
				} else if submittedBy != identity.Identity {
					submitterOK = false
				}
			}
		}
	}
	latest, err := c.inventory.Lookup(rootRef.GVK)
	if err != nil || !sameSelection(latest, expected) {
		return discovery.ErrStale
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if scopeRevision != c.scopeRevision || definitionRevision != c.definitionRevision ||
		!c.namespaces[rootRef.Namespace] || !c.discovery.IsCurrent(tokens...) {
		return discovery.ErrStale
	}
	rootEvidence, rootVerified := c.evidenceLocked(append(slices.Clone(workload.Path), rootRef))
	if c.namespaceEvidence.ExpiresAt.IsZero() || !c.options.Clock().Before(c.namespaceEvidence.ExpiresAt) {
		rootVerified = false
	} else {
		rootEvidence.ExpiresAt = earlier(rootEvidence.ExpiresAt, c.namespaceEvidence.ExpiresAt)
		rootEvidence.Dependencies = append(rootEvidence.Dependencies, c.namespaceEvidence.Dependencies...)
	}
	interpretationEvidence := rootEvidence
	interpretationVerified := rootVerified && !c.definitionEvidence.ExpiresAt.IsZero() && c.options.Clock().Before(c.definitionEvidence.ExpiresAt)
	if interpretationVerified {
		interpretationEvidence.ExpiresAt = earlier(interpretationEvidence.ExpiresAt, c.definitionEvidence.ExpiresAt)
		interpretationEvidence.Dependencies = append(slices.Clone(interpretationEvidence.Dependencies), c.definitionEvidence.Dependencies...)
	}
	record := &store.Publication{Workload: store.WorkloadRecord{
		UID: uid, Ref: gvkRef(rootRef.GVK, rootRef.Namespace, rootRef.Name),
		OwnerResolution:  string(workload.Resolution),
		CreatedTimestamp: root.GetCreationTimestamp().Unix(),
	}}
	if root.GetCreationTimestamp().Time.IsZero() {
		record.Workload.CreatedTimestamp = 0
	}
	if rootVerified {
		record.Creation = rootEvidence
	}
	if current := c.store.Published(uid); current != nil {
		if !rootVerified {
			record.Creation = current.Creation
		}
		if !interpretationVerified {
			record.Status = current.Status
			record.Workload.HasStatus = current.Workload.HasStatus
			record.Workload.Phases = current.Workload.Phases
		}
	}
	history, hasHistory := c.history[uid]
	if hasHistory && history.selection != expected.Origin {
		hasHistory = false
	}
	if interpretation != nil {
		record.Workload.Karta = expected.Origin.Name
		record.Workload.KartaSource = string(expected.Origin.Source)
		if statusErr != nil && !errors.Is(statusErr, discovery.ErrPending) && !errors.Is(statusErr, discovery.ErrStale) {
			record.FailureReasons = append(record.FailureReasons, runtimeFailureReason(statusErr))
		}
		if statusErr == nil && status.Defined && interpretationVerified {
			record.Workload.HasStatus = true
			record.Workload.Phases = slices.Clone(status.Matched)
			record.Status = interpretationEvidence
		} else if statusErr != nil && hasHistory && !errors.Is(statusErr, discovery.ErrPending) &&
			!errors.Is(statusErr, discovery.ErrStale) && !errors.Is(statusErr, discovery.ErrInvalidDefinition) {
			record.Workload.HasStatus = history.record.Workload.HasStatus
			record.Workload.Phases = slices.Clone(history.record.Workload.Phases)
			record.Status = store.Evidence{Reason: runtimeFailureReason(statusErr)}
		}
	}
	allFresh := interpretationVerified
	var publishedPods []store.PublishedPod
	current := c.store.Published(uid)
	currentRows := publicationRows(current)
	var historyRows map[types.UID]store.PublishedPod
	if hasHistory {
		historyRows = publicationRows(history.record)
	}
	for _, item := range evaluated {
		if item.err != nil && !errors.Is(item.err, discovery.ErrPending) && !errors.Is(item.err, discovery.ErrStale) {
			reason := runtimeFailureReason(item.err)
			if !slices.Contains(record.FailureReasons, reason) {
				record.FailureReasons = append(record.FailureReasons, reason)
			}
		}
		if previous := c.associations[item.ref.UID]; previous != "" && previous != uid {
			c.unlinkPodLocked(item.ref.UID)
			if old := c.store.Published(previous); old != nil {
				c.store.Publish(old.WithPod(item.ref.UID, nil))
			}
			if oldHistory, exists := c.history[previous]; exists {
				oldHistory.record = oldHistory.record.WithPod(item.ref.UID, nil)
				c.history[previous] = oldHistory
			}
		}
		c.associations[item.ref.UID] = uid
		if c.podsByRoot[uid] == nil {
			c.podsByRoot[uid] = map[types.UID]struct{}{}
		}
		c.podsByRoot[uid][item.ref.UID] = struct{}{}
		if !item.visible {
			continue
		}
		evidence, verified := c.evidenceLocked(append(slices.Clone(item.path), rootRef))
		verified = verified && interpretationVerified
		if verified {
			evidence.ExpiresAt = earlier(evidence.ExpiresAt, interpretationEvidence.ExpiresAt)
			evidence.Dependencies = append(evidence.Dependencies, interpretationEvidence.Dependencies...)
		}
		if item.err == nil && verified {
			row := store.PublishedPod{Record: store.PodRecord{
				UID: item.ref.UID, Namespace: item.ref.Namespace, Name: item.ref.Name,
				WorkloadUID: uid, Workload: record.Workload.Ref, OwnerResolution: string(workload.Resolution),
				Component: item.mapping.Component, Instance: item.mapping.Instance, Replica: item.mapping.Group,
			}, Mapping: evidence}
			if interpretation == nil {
				row.Record.Component, row.Record.Instance = "<unknown>", "<unknown>"
				row.Record.Workload = gvkRef(item.root.GVK, item.root.Namespace, item.root.Name)
			}
			publishedPods = append(publishedPods, row)
		} else {
			allFresh = false
			if item.err == nil {
				if row, exists := currentRows[item.ref.UID]; exists {
					publishedPods = append(publishedPods, row)
				}
			}
			if item.err != nil && hasHistory && !errors.Is(item.err, discovery.ErrPending) &&
				!errors.Is(item.err, discovery.ErrStale) && !errors.Is(item.err, discovery.ErrInvalidDefinition) {
				if old, exists := historyRows[item.ref.UID]; exists && old.Record.WorkloadUID == uid {
					old.Mapping = store.Evidence{Reason: runtimeFailureReason(item.err)}
					if item.ownerErr {
						old.Record.OwnerResolution = "unknown"
					}
					publishedPods = append(publishedPods, old)
				}
			}
		}
	}
	record.PodPages = store.BuildPodPages(publishedPods, current)
	if proof, exists := c.membership[rootRef.Namespace]; exists &&
		proof.revision == c.membershipRevision[rootRef.Namespace] && rootVerified && submitterOK {
		listed := proof.pods[uid]
		observed := map[types.UID]bool{}
		for _, item := range evaluated {
			observed[item.ref.UID] = true
		}
		equal := len(listed) == len(observed)
		for _, ref := range listed {
			equal = equal && observed[ref.UID]
		}
		if equal {
			record.Membership = proof.evidence
			record.Membership.ExpiresAt = earlier(record.Membership.ExpiresAt, rootEvidence.ExpiresAt)
			record.Membership.Dependencies = append(slices.Clone(record.Membership.Dependencies), rootEvidence.Dependencies...)
			record.SubmittedBy = submittedBy
		}
	}
	c.store.Publish(record)
	// Failed recomputation may display history but cannot replace the last
	// verified interpretation used by a later failure.
	nextHistory := record
	if hasHistory {
		nextHistory = mergeVerifiedHistory(history.record, record, evaluated, statusErr, rootVerified)
	}
	c.history[uid] = rootHistory{selection: expected.Origin, record: nextHistory}
	if c.rootsByNamespace[rootRef.Namespace] == nil {
		c.rootsByNamespace[rootRef.Namespace] = map[types.UID]struct{}{}
	}
	c.rootsByNamespace[rootRef.Namespace][uid] = struct{}{}
	if !allFresh {
		return discovery.ErrPending
	}
	return nil
}

func (c *Runtime) publishSelectionFailure(ctx context.Context, workload discovery.GenericWorkload, ref discovery.ObjectRef, cause error) {
	root, err := c.reader.Get(ctx, ref)
	if err != nil || ref.Namespace == "" {
		return
	}
	selected, err := c.rootSelected(ref, root)
	if err != nil || !selected {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.namespaces[ref.Namespace] || !c.discovery.IsCurrent(workload.State()) {
		return
	}
	evidence, verified := c.evidenceLocked(append(slices.Clone(workload.Path), ref))
	record := &store.Publication{Workload: store.WorkloadRecord{
		UID: ref.UID, Ref: gvkRef(ref.GVK, ref.Namespace, ref.Name), OwnerResolution: string(workload.Resolution),
		CreatedTimestamp: root.GetCreationTimestamp().Unix(),
	}, FailureReasons: []string{runtimeFailureReason(cause)}}
	if root.GetCreationTimestamp().Time.IsZero() {
		record.Workload.CreatedTimestamp = 0
	}
	if verified && !c.namespaceEvidence.ExpiresAt.IsZero() && c.options.Clock().Before(c.namespaceEvidence.ExpiresAt) {
		evidence.ExpiresAt = earlier(evidence.ExpiresAt, c.namespaceEvidence.ExpiresAt)
		evidence.Dependencies = append(evidence.Dependencies, c.namespaceEvidence.Dependencies...)
		record.Creation = evidence
	}
	if history, exists := c.history[ref.UID]; exists && !errors.Is(cause, discovery.ErrInvalidDefinition) {
		record.Workload.HasStatus = history.record.Workload.HasStatus
		record.Workload.Phases = history.record.Workload.Phases
		record.Workload.Karta, record.Workload.KartaSource = history.record.Workload.Karta, history.record.Workload.KartaSource
		record.Status = store.Evidence{Reason: runtimeFailureReason(cause)}
		var rows []store.PublishedPod
		for _, page := range history.record.PodPages {
			for _, row := range page.Rows {
				row.Mapping = store.Evidence{Reason: runtimeFailureReason(cause)}
				rows = append(rows, row)
			}
		}
		record.PodPages = store.BuildPodPages(rows, nil)
	}
	c.store.Publish(record)
	if c.rootsByNamespace[ref.Namespace] == nil {
		c.rootsByNamespace[ref.Namespace] = map[types.UID]struct{}{}
	}
	c.rootsByNamespace[ref.Namespace][ref.UID] = struct{}{}
}

func mergeVerifiedHistory(previous, current *store.Publication, pods []evaluatedPod, statusErr error, rootVerified bool) *store.Publication {
	merged := *current
	if statusErr != nil || !rootVerified {
		merged.Workload.HasStatus = previous.Workload.HasStatus
		merged.Workload.Phases = previous.Workload.Phases
	}
	currentRows := publicationRows(current)
	previousRows := publicationRows(previous)
	for _, item := range pods {
		if !item.visible {
			continue
		}
		if _, exists := currentRows[item.ref.UID]; exists && item.err == nil {
			continue
		}
		if old, exists := previousRows[item.ref.UID]; exists {
			currentRows[item.ref.UID] = old
		}
	}
	rows := make([]store.PublishedPod, 0, len(currentRows))
	for _, row := range currentRows {
		rows = append(rows, row)
	}
	merged.PodPages = store.BuildPodPages(rows, previous)
	return &merged
}

func publicationRows(record *store.Publication) map[types.UID]store.PublishedPod {
	rows := map[types.UID]store.PublishedPod{}
	if record != nil {
		for _, page := range record.PodPages {
			for _, row := range page.Rows {
				rows[row.Record.UID] = row
			}
		}
	}
	return rows
}

func sameSelection(a, b kartadefinitioninventory.Selection) bool {
	return a.Origin == b.Origin && a.GVK == b.GVK && reflect.DeepEqual(a.Definition, b.Definition)
}

func (c *Runtime) rootSelected(ref discovery.ObjectRef, root *unstructured.Unstructured) (bool, error) {
	c.mu.Lock()
	fact, exists := c.reader.objects[refKey(ref)]
	c.mu.Unlock()
	if !exists || fact.observation.Ref != ref {
		return false, discovery.ErrStale
	}
	if !c.objectSelector.Matches(labels.Set(root.GetLabels())) {
		return false, nil
	}
	resource := fact.observation.WatchKey.Resource
	for _, rule := range c.options.ExcludeResourceRules {
		if resourceRuleMatches(rule, resource) {
			return false, nil
		}
	}
	if len(c.options.ResourceRules) == 0 {
		return true, nil
	}
	for _, rule := range c.options.ResourceRules {
		if resourceRuleMatches(rule, resource) {
			return true, nil
		}
	}
	return false, nil
}

func resourceRuleMatches(rule ResourceRule, resource schema.GroupVersionResource) bool {
	return (slices.Contains(rule.APIGroups, "*") || slices.Contains(rule.APIGroups, resource.Group)) &&
		(slices.Contains(rule.APIVersions, "*") || slices.Contains(rule.APIVersions, resource.Version)) &&
		(slices.Contains(rule.Resources, "*") || slices.Contains(rule.Resources, resource.Resource))
}

func (c *Runtime) retainPodFailure(ref discovery.ObjectRef, err error, resolution discovery.OwnerResolution) {
	c.mu.Lock()
	defer c.mu.Unlock()
	root := c.associations[ref.UID]
	history, exists := c.history[root]
	if !exists || c.reader.objects[refKey(ref)].observation.Ref != ref {
		return
	}
	row, exists := history.record.Pod(ref.UID)
	if !exists {
		return
	}
	record := c.store.Published(root)
	if record == nil {
		return
	}
	row.Mapping = store.Evidence{Reason: runtimeFailureReason(err)}
	row.Record.OwnerResolution = string(resolution)
	c.store.Publish(record.WithPod(ref.UID, &row))
}

func (c *Runtime) retainRootFailure(uid types.UID, err error) {
	if errors.Is(err, discovery.ErrPending) || errors.Is(err, discovery.ErrStale) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	record := c.store.Published(uid)
	history, exists := c.history[uid]
	if record == nil || !exists {
		return
	}
	next := *record
	// Resolve did not verify this root; selection failures use a separate path.
	next.Workload.OwnerResolution = string(discovery.UnknownOwner)
	next.FailureReasons = []string{runtimeFailureReason(err)}
	next.Membership = store.Evidence{}
	next.SubmittedBy = ""
	if errors.Is(err, discovery.ErrInvalidDefinition) {
		next.Workload.HasStatus = false
		next.PodPages = nil
		delete(c.history, uid)
	} else {
		next.Workload.HasStatus = history.record.Workload.HasStatus
		next.Workload.Phases = history.record.Workload.Phases
		next.Status = store.Evidence{Reason: runtimeFailureReason(err)}
		var rows []store.PublishedPod
		for _, page := range history.record.PodPages {
			for _, row := range page.Rows {
				row.Mapping = store.Evidence{Reason: runtimeFailureReason(err)}
				row.Record.OwnerResolution = string(discovery.UnknownOwner)
				rows = append(rows, row)
			}
		}
		next.PodPages = store.BuildPodPages(rows, record)
	}
	c.store.Publish(&next)
}
