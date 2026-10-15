// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
)

// Component is an immutable view of a component and its descendants.
type Component struct{ scope scope }

// Instance binds a component's declared instance ID, preserving outer axes.
// The empty string denotes an anonymous instance.
type Instance struct{ scope scope }

// Replica binds the nearest replica axis, preserving all outer axes.
type Replica struct{ scope scope }

type scope struct {
	workload  *Workload
	component string
	bindings  []postingKey
}

func (c *Component) Pods() ([]*corev1.Pod, error)             { return c.scope.pods() }
func (i *Instance) Pods() ([]*corev1.Pod, error)              { return i.scope.pods() }
func (r *Replica) Pods() ([]*corev1.Pod, error)               { return r.scope.pods() }
func (c *Component) Instance(id string) (*Instance, error)    { return c.scope.instance(id) }
func (c *Component) Replica(key string) (*Replica, error)     { return c.scope.replica(key) }
func (i *Instance) Component(name string) (*Component, error) { return i.scope.child(name) }
func (i *Instance) Replica(key string) (*Replica, error)      { return i.scope.replica(key) }
func (r *Replica) Component(name string) (*Component, error)  { return r.scope.child(name) }

func (s scope) instance(id string) (*Instance, error) {
	ids := []string{""}
	if s.workload.prepared != nil {
		var err error
		ids, err = s.workload.prepared.InstanceIDs(s.component)
		if err != nil {
			return nil, fmt.Errorf("%w: declared instances for %s: %w", ErrIncompleteAttribution, s.component, err)
		}
	}
	if !slices.Contains(ids, id) {
		return nil, fmt.Errorf("%w: %s/%s", ErrInstanceNotFound, s.component, id)
	}
	next := s.bind(postingKey{component: s.component, axis: "instance", value: id})
	return &Instance{scope: next}, nil
}

func (s scope) replica(key string) (*Replica, error) {
	path := s.workload.paths[s.component]
	for i := len(path) - 1; i >= 0; i-- {
		component := s.workload.components[path[i]]
		if component.PodSelector != nil && component.PodSelector.ReplicaSelector != nil {
			for _, binding := range s.bindings {
				if binding.component == path[i] && binding.axis == "replica" && binding.value != key {
					return nil, fmt.Errorf("%w: replica axis %s is already bound to %q", ErrInvalidInput, path[i], binding.value)
				}
			}
			return &Replica{scope: s.bind(postingKey{component: path[i], axis: "replica", value: key})}, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNoReplicaAxis, s.component)
}

func (s scope) bind(binding postingKey) scope {
	result := s
	result.bindings = slices.Clone(s.bindings)
	// Rebinding the same axis is explicit; outer axes remain intact.
	for i, existing := range result.bindings {
		if existing.component == binding.component && existing.axis == binding.axis {
			result.bindings[i] = binding
			return result
		}
	}
	result.bindings = append(result.bindings, binding)
	return result
}

func (s scope) child(name string) (*Component, error) {
	if !slices.Contains(s.workload.paths[name], s.component) || name == s.component {
		return nil, fmt.Errorf("%w: %s is not a descendant of %s", ErrComponentNotFound, name, s.component)
	}
	next := s
	next.component = name
	return &Component{scope: next}, nil
}

func (s scope) pods() ([]*corev1.Pod, error) {
	w := s.workload
	// Failed classification cannot silently remove possible members. Errors on
	// unrelated components or axes that the caller did not bind are harmless.
	for _, row := range w.issueRows {
		if s.uncertain(w.memberships[row]) {
			return nil, fmt.Errorf("%w: Pod %s may belong to requested %s scope", ErrIncompleteAttribution, w.pods[row].Name, s.component)
		}
	}
	keys := append([]postingKey{{component: s.component}}, s.bindings...)
	var smallest []int
	for i, key := range keys {
		rows := w.postings[key]
		if i == 0 || len(rows) < len(smallest) {
			smallest = rows
		}
	}
	result := make([]*corev1.Pod, 0, len(smallest))
	for _, row := range smallest {
		include := true
		for _, key := range keys {
			if _, ok := slices.BinarySearch(w.postings[key], row); !ok {
				include = false
				break
			}
		}
		if include {
			result = append(result, w.pods[row].DeepCopy())
		}
	}
	return result, nil
}

func (s scope) uncertain(m attribution.Membership) bool {
	w := s.workload
	if m.Component == "" {
		for _, issue := range m.Issues {
			if issue.Axis == "component" && (issue.Component == "" || slices.Contains(w.paths[issue.Component], s.component)) {
				return true
			}
		}
		return false
	}
	if !slices.Contains(w.paths[m.Component], s.component) {
		return false
	}
	unresolved := false
	for _, binding := range s.bindings {
		var value *string
		for _, coordinate := range m.Path {
			if coordinate.Component == binding.component {
				switch binding.axis {
				case "instance":
					value = coordinate.Instance
				case "replica":
					value = coordinate.Replica
				}
				break
			}
		}
		if value != nil {
			if *value != binding.value {
				return false
			}
			continue
		}
		failed := false
		for _, issue := range m.Issues {
			if issue.Component == binding.component && issue.Axis == binding.axis {
				failed = true
				break
			}
		}
		// jq empty (no applicable replica) cannot match a bound replica key.
		if !failed {
			return false
		}
		unresolved = true
	}
	return unresolved
}
