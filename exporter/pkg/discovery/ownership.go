// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"maps"
	"reflect"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Upsert accepts facts from the registered watch run, including its initial list.
// The caller must also fence callbacks under its publication lock.
func (d *Discovery) Upsert(object ObservedObject) ChangeSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	coverage, ok := d.coverage[object.WatchKey]
	if !ok || coverage.Run != object.Run || coverage.Removed || object.Ref.UID == "" {
		return ChangeSet{}
	}
	ref := object.Ref
	name := objectName{ref.GVK, ref.Namespace, ref.Name}
	affected := make(map[types.UID]struct{})
	objects := make(map[types.UID]struct{})
	var retired ChangeSet
	if oldUID := d.ownership.byName[name]; oldUID != "" && oldUID != ref.UID {
		d.descendantsLocked(oldUID, objects)
		maps.Copy(affected, objects)
		d.ancestorsLocked(oldUID, affected)
		retired = d.changeSetLocked(affected, objects)
		d.deleteLocked(oldUID)
	}
	d.descendantsLocked(ref.UID, objects)
	maps.Copy(affected, objects)
	node := d.ownership.nodes[ref.UID]
	if node == nil {
		d.revision++
		node = &OwnerNode{
			objects:      make(map[schema.GroupVersionKind]observation),
			parents:      make(map[types.UID]struct{}),
			edgeRevision: d.revision, membershipRevision: d.revision,
		}
		d.ownership.nodes[ref.UID] = node
	}
	old, exists := node.objects[ref.GVK]
	if exists && old.Ref == ref && old.WatchKey == object.WatchKey && old.Run == object.Run && reflect.DeepEqual(old.Owners, object.Owners) {
		return mergeChanges(retired, d.changeSetLocked(affected, objects))
	}
	d.ancestorsLocked(ref.UID, affected)
	object.Owners = slices.Clone(object.Owners)
	for i := range object.Owners {
		object.Owners[i] = *object.Owners[i].DeepCopy()
	}
	d.revision++
	node.objects[ref.GVK] = observation{ObservedObject: object, revision: d.revision}
	d.ownership.byName[name] = ref.UID
	if d.ownership.byKind[ref.GVK.GroupKind()] == nil {
		d.ownership.byKind[ref.GVK.GroupKind()] = make(map[types.UID]struct{})
	}
	d.ownership.byKind[ref.GVK.GroupKind()][ref.UID] = struct{}{}
	edgeChanged := !exists || !reflect.DeepEqual(old.Owners, object.Owners)
	if edgeChanged {
		d.relinkLocked(ref.UID, node)
		d.ancestorsLocked(ref.UID, affected)
		for uid := range affected {
			if ancestor := d.ownership.nodes[uid]; ancestor != nil {
				ancestor.membershipRevision = d.revision
			}
		}
	}
	d.descendantsLocked(ref.UID, objects)
	maps.Copy(affected, objects)
	return mergeChanges(retired, d.changeSetLocked(affected, objects))
}

func (d *Discovery) relinkLocked(uid types.UID, node *OwnerNode) {
	for parent := range node.parents {
		delete(d.ownership.children[parent], uid)
		if len(d.ownership.children[parent]) == 0 {
			delete(d.ownership.children, parent)
		}
	}
	node.parents = make(map[types.UID]struct{})
	for _, object := range node.objects {
		for _, owner := range object.Owners {
			if owner.Controller == nil || !*owner.Controller {
				continue
			}
			gv, err := schema.ParseGroupVersion(owner.APIVersion)
			if err == nil {
				if _, ignored := d.ownership.ignoredOwners[gv.WithKind(owner.Kind)]; ignored {
					continue
				}
			}
			node.parents[owner.UID] = struct{}{}
			if d.ownership.children[owner.UID] == nil {
				d.ownership.children[owner.UID] = make(map[types.UID]struct{})
			}
			d.ownership.children[owner.UID][uid] = struct{}{}
		}
	}
	node.edgeRevision = d.revision
}

// Delete retires exactly this UID, preserving descendants as unresolved nodes.
// Watch-run and deletion-guard checks belong to the calling controller.
func (d *Discovery) Delete(ref ObjectRef) ChangeSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	node := d.ownership.nodes[ref.UID]
	if node == nil {
		return ChangeSet{}
	}
	observed, ok := node.objects[ref.GVK]
	if !ok || observed.Ref.Name != ref.Name || observed.Ref.Namespace != ref.Namespace {
		return ChangeSet{}
	}
	objects := make(map[types.UID]struct{})
	d.descendantsLocked(ref.UID, objects)
	affected := maps.Clone(objects)
	d.ancestorsLocked(ref.UID, affected)
	changes := d.changeSetLocked(affected, objects)
	d.deleteLocked(ref.UID)
	for uid := range affected {
		if remaining := d.ownership.nodes[uid]; remaining != nil {
			remaining.membershipRevision = d.revision
		}
	}
	return mergeChanges(changes, d.changeSetLocked(affected, objects))
}

func (d *Discovery) deleteLocked(uid types.UID) {
	node := d.ownership.nodes[uid]
	if node == nil {
		return
	}
	for _, object := range node.objects {
		ref := object.Ref
		key := objectName{ref.GVK, ref.Namespace, ref.Name}
		if d.ownership.byName[key] == uid {
			delete(d.ownership.byName, key)
		}
		delete(d.ownership.byKind[ref.GVK.GroupKind()], uid)
		if len(d.ownership.byKind[ref.GVK.GroupKind()]) == 0 {
			delete(d.ownership.byKind, ref.GVK.GroupKind())
		}
	}
	for parent := range node.parents {
		delete(d.ownership.children[parent], uid)
		if len(d.ownership.children[parent]) == 0 {
			delete(d.ownership.children, parent)
		}
	}
	delete(d.ownership.nodes, uid)
	d.revision++
}

func (d *Discovery) DefinitionChanged(kind schema.GroupKind) ChangeSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.revision++
	d.definitions[kind] = d.revision
	affected := make(map[types.UID]struct{})
	for uid := range d.ownership.byKind[kind] {
		d.descendantsLocked(uid, affected)
	}
	return d.changeSetLocked(affected, affected)
}

func (d *Discovery) CoverageChanged(change CoverageChange) ChangeSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	old, ok := d.coverage[change.Key]
	if ok && old.Run > change.Run {
		return ChangeSet{}
	}
	d.revision++
	d.coverage[change.Key] = coverageState{CoverageChange: change, revision: d.revision}
	affected := make(map[types.UID]struct{})
	objects := make(map[types.UID]struct{})
	var retired []ObjectRef
	for uid, node := range d.ownership.nodes {
		for _, object := range node.objects {
			if object.WatchKey == change.Key {
				d.descendantsLocked(uid, objects)
				d.ancestorsLocked(uid, affected)
				if change.Removed || change.Ready && change.Err == nil && object.Run != change.Run {
					retired = append(retired, object.Ref)
				}
			}
		}
		// Empty newly synchronized stores must also wake children awaiting owners.
		for parent := range node.parents {
			if d.ownership.nodes[parent] == nil {
				d.descendantsLocked(uid, objects)
			}
		}
	}
	maps.Copy(affected, objects)
	changes := d.changeSetLocked(affected, objects)
	for _, ref := range retired {
		node := d.ownership.nodes[ref.UID]
		if len(node.objects) == 1 {
			d.deleteLocked(ref.UID)
			continue
		}
		delete(node.objects, ref.GVK)
		delete(d.ownership.byName, objectName{ref.GVK, ref.Namespace, ref.Name})
		hasKind := false
		for gvk := range node.objects {
			if gvk.GroupKind() == ref.GVK.GroupKind() {
				hasKind = true
				break
			}
		}
		if !hasKind {
			delete(d.ownership.byKind[ref.GVK.GroupKind()], ref.UID)
		}
		d.revision++
		d.relinkLocked(ref.UID, node)
	}
	if len(retired) > 0 {
		for uid := range affected {
			if node := d.ownership.nodes[uid]; node != nil {
				node.membershipRevision = d.revision
			}
		}
	}
	return mergeChanges(changes, d.changeSetLocked(affected, objects))
}

func mergeChanges(before, after ChangeSet) ChangeSet {
	after.Objects = uniqueRefs(append(before.Objects, after.Objects...))
	after.Roots = uniqueRefs(append(before.Roots, after.Roots...))
	return after
}

func (d *Discovery) descendantsLocked(uid types.UID, result map[types.UID]struct{}) {
	pending := []types.UID{uid}
	visited := make(map[types.UID]struct{})
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, seen := visited[current]; seen {
			continue
		}
		visited[current] = struct{}{}
		result[current] = struct{}{}
		for child := range d.ownership.children[current] {
			pending = append(pending, child)
		}
	}
}

func (d *Discovery) ancestorsLocked(uid types.UID, result map[types.UID]struct{}) {
	pending := []types.UID{uid}
	visited := make(map[types.UID]struct{})
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, seen := visited[current]; seen {
			continue
		}
		visited[current] = struct{}{}
		result[current] = struct{}{}
		if node := d.ownership.nodes[current]; node != nil {
			for parent := range node.parents {
				pending = append(pending, parent)
			}
		}
	}
}

func (d *Discovery) changeSetLocked(affected, objects map[types.UID]struct{}) ChangeSet {
	var changes ChangeSet
	missing := make(map[CoverageRequest]struct{})
	for uid := range affected {
		node := d.ownership.nodes[uid]
		if node == nil {
			continue
		}
		for _, object := range node.objects {
			if _, changed := objects[uid]; changed {
				changes.Objects = append(changes.Objects, object.Ref)
			}
			_, parent, err := d.parentLocked(node, object.Ref)
			if err != nil {
				continue
			}
			if parent == nil {
				changes.Roots = append(changes.Roots, object.Ref)
			} else if parentNode := d.ownership.nodes[parent.UID]; parentNode == nil {
				missing[CoverageRequest{GVK: parent.GVK, Namespace: parent.Namespace}] = struct{}{}
			} else if _, ok := parentNode.objects[parent.GVK]; !ok {
				missing[CoverageRequest{GVK: parent.GVK, Namespace: parent.Namespace}] = struct{}{}
			}
		}
	}
	changes.Objects = uniqueRefs(changes.Objects)
	changes.Roots = uniqueRefs(changes.Roots)
	changes.MissingCoverage = slices.Collect(maps.Keys(missing))
	return changes
}

func uniqueRefs(refs []ObjectRef) []ObjectRef {
	slices.SortFunc(refs, compareRefs)
	return slices.Compact(refs)
}

func compareRefs(a, b ObjectRef) int {
	for _, pair := range [][2]string{
		{string(a.UID), string(b.UID)}, {a.GVK.String(), b.GVK.String()},
		{a.Namespace, b.Namespace}, {a.Name, b.Name}, {a.ResourceVersion, b.ResourceVersion},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func controllerOwner(owners []metav1.OwnerReference, namespace string) (*OwnerKey, error) {
	var parent *OwnerKey
	for _, owner := range owners {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		if parent != nil || owner.UID == "" || owner.Name == "" || owner.Kind == "" {
			return nil, ErrUnresolvedOwner
		}
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil || gv.Version == "" {
			return nil, ErrUnresolvedOwner
		}
		parent = &OwnerKey{GVK: gv.WithKind(owner.Kind), Namespace: namespace, Name: owner.Name, UID: owner.UID}
	}
	return parent, nil
}
