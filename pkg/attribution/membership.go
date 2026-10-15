// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package attribution

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

// Membership contains the selected producer and every coordinate from the
// logical root through that producer. Issues retain ownership-confirmed Pods
// when some membership fields cannot be established.
type Membership struct {
	Component string
	Path      []Coordinate
	Issues    []Issue
}

// Coordinate binds a component's own axes. A pointer to the empty string is an
// anonymous instance. Nil means the axis is absent or could not be resolved;
// Issues distinguish unresolved axes from absent ones.
type Coordinate struct {
	Component string
	Instance  *string
	Replica   *string
}

// Issue describes an unresolved component, instance, or replica axis. An empty
// Component means that the Pod could belong to any component.
type Issue struct {
	PodUID    types.UID
	Component string
	Axis      string
	Cause     error
}

// Evaluate resolves observed membership with strict producer selectors. It
// evaluates all explicit selectors, even for a sole producer, and chooses the
// unique most-specific matching component. No desired-state extraction occurs.
func Evaluate(ctx context.Context, pod *corev1.Pod, p *Prepared) (Membership, error) {
	if err := ctx.Err(); err != nil {
		return Membership{}, err
	}
	if pod == nil {
		return Membership{}, fmt.Errorf("pod is nil")
	}
	if p == nil || p.plan == nil {
		return Membership{}, fmt.Errorf("prepared attribution is nil")
	}
	plan := p.plan
	querier := resource.NewPodQuerier(pod)
	var result Membership
	var matches, failed []string
	for _, name := range plan.producers {
		if err := ctx.Err(); err != nil {
			return Membership{}, err
		}
		definition := plan.definitions[name]
		var selector *v1alpha1.ComponentTypeSelector
		if definition.PodSelector != nil {
			selector = definition.PodSelector.ComponentTypeSelector
		}
		if selector == nil {
			if len(plan.producers) == 1 {
				matches = append(matches, name)
			}
			continue
		}
		matched, err := querier.MatchesComponentType(ctx, selector)
		if cancellation := ctx.Err(); cancellation != nil {
			return Membership{}, cancellation
		}
		if err != nil {
			failed = append(failed, name)
			result.Issues = append(result.Issues, Issue{PodUID: pod.UID, Component: name, Axis: "component", Cause: err})
		} else if matched {
			matches = append(matches, name)
		}
	}

	// Matching ancestors are redundant; unrelated matches remain ambiguous.
	var candidates []string
	for _, name := range matches {
		mostSpecific := true
		for _, other := range matches {
			if other != name && slices.Contains(plan.paths[other], name) {
				mostSpecific = false
				break
			}
		}
		if mostSpecific {
			candidates = append(candidates, name)
		}
	}
	switch len(candidates) {
	case 0:
		if len(failed) == 0 {
			result.Issues = append(result.Issues, Issue{PodUID: pod.UID, Axis: "component", Cause: fmt.Errorf("no component selector matches pod")})
		}
		return result, nil
	case 1:
		name := candidates[0]
		for _, unresolved := range failed {
			if !slices.Contains(plan.paths[name], unresolved) {
				result.Issues = append(result.Issues, Issue{PodUID: pod.UID, Component: name, Axis: "component", Cause: fmt.Errorf("component %q may compete with unresolved selector for %q", name, unresolved)})
				return result, nil
			}
		}
		result.Component = name
	default:
		for _, name := range candidates {
			result.Issues = append(result.Issues, Issue{PodUID: pod.UID, Component: name, Axis: "component", Cause: fmt.Errorf("ambiguous component selectors match %v", candidates)})
		}
		return result, nil
	}

	for _, name := range plan.paths[result.Component] {
		if err := ctx.Err(); err != nil {
			return Membership{}, err
		}
		definition := plan.definitions[name]
		coordinate := Coordinate{Component: name}
		var instanceSelector *v1alpha1.ComponentInstanceSelector
		var replicaSelector *v1alpha1.ReplicaSelector
		if definition.PodSelector != nil {
			instanceSelector = definition.PodSelector.ComponentInstanceSelector
			replicaSelector = definition.PodSelector.ReplicaSelector
		}
		if failure, ok := p.failures[name]; ok {
			result.Issues = append(result.Issues, Issue{PodUID: pod.UID, Component: name, Axis: "instance", Cause: failure})
		} else {
			instance, err := querier.GetMatchingInstanceId(ctx, instanceSelector, p.ids[name])
			if cancellation := ctx.Err(); cancellation != nil {
				return Membership{}, cancellation
			}
			if err != nil {
				result.Issues = append(result.Issues, Issue{PodUID: pod.UID, Component: name, Axis: "instance", Cause: err})
			} else {
				coordinate.Instance = &instance
			}
		}
		if replicaSelector != nil {
			replica, found, err := querier.ExtractReplicaKey(ctx, replicaSelector)
			if cancellation := ctx.Err(); cancellation != nil {
				return Membership{}, cancellation
			}
			if err != nil {
				result.Issues = append(result.Issues, Issue{PodUID: pod.UID, Component: name, Axis: "replica", Cause: err})
			} else if found {
				coordinate.Replica = &replica
			}
		}
		result.Path = append(result.Path, coordinate)
	}
	return result, nil
}
