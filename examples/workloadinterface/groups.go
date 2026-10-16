// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

// SchedulingGroup contains one workload's Pods with the same PodGroup name and
// grouping keys. Different member components can belong to the same group.
type SchedulingGroup struct {
	Name string
	Keys []string
	Pods []*corev1.Pod
}

// SchedulingGroups evaluates the catalog's gangScheduling.podGroups instructions
// on one captured workload. It returns deterministic groups of detached full Pods.
// Generic workloads, singular podGroup instructions and owner-walk fallback
// without explicit grouping keys return ErrUnsupported.
// Unresolved attribution or grouping returns an error, never partial groups.
// This consumer example re-evaluates attribution; it performs no cluster reads.
func SchedulingGroups(ctx context.Context, observed *discovery.Workload) ([]SchedulingGroup, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if observed == nil {
		return nil, fmt.Errorf("%w: workload is nil", discovery.ErrInvalidInput)
	}
	definition := observed.Definition()
	if definition == nil {
		return nil, discovery.ErrUnsupported
	}
	if gang := definition.Spec.Instructions.GangScheduling; gang != nil && gang.PodGroup != nil {
		return nil, fmt.Errorf("%w: singular podGroup instructions", discovery.ErrUnsupported)
	}
	plan, err := attribution.Compile(definition)
	if err != nil {
		return nil, err
	}
	prepared, err := attribution.Prepare(ctx, plan, observed.Object())
	if err != nil {
		return nil, err
	}
	summary, err := instructions.NewStructureSummary(definition)
	if err != nil {
		return nil, err
	}
	groups := make(map[string]*SchedulingGroup)
	for _, pod := range observed.Pods() {
		membership, err := attribution.Evaluate(ctx, pod, prepared)
		if err != nil {
			return nil, err
		}
		if len(membership.Issues) > 0 {
			return nil, fmt.Errorf("%w: Pod %s/%s has unresolved %s attribution",
				discovery.ErrIncompleteAttribution, pod.Namespace, pod.Name, membership.Issues[0].Axis)
		}
		querier := resource.NewPodQuerier(pod)
		group, err := instructions.GetPodGroupingEffectiveComponent(ctx, querier, membership.Component, summary)
		if err != nil {
			return nil, fmt.Errorf("group Pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		if group == nil {
			continue
		}
		if len(group.MemberDefinition.GroupByKeyPaths) == 0 {
			return nil, fmt.Errorf("%w: group %s requires owner-walk fallback", discovery.ErrUnsupported, group.PodGroupName)
		}
		keys, err := querier.ExtractGroupKeys(ctx, group.MemberDefinition.GroupByKeyPaths)
		if err != nil {
			return nil, fmt.Errorf("group keys for Pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		// A tuple preserves key boundaries. The map belongs to one workload;
		// member components are not part of the gang identity.
		encoded, err := json.Marshal([]any{group.PodGroupName, keys})
		if err != nil {
			return nil, fmt.Errorf("encode grouping keys: %w", err)
		}
		key := string(encoded)
		if groups[key] == nil {
			groups[key] = &SchedulingGroup{Name: group.PodGroupName, Keys: slices.Clone(keys)}
		}
		groups[key].Pods = append(groups[key].Pods, pod)
	}
	result := make([]SchedulingGroup, 0, len(groups))
	for _, group := range groups {
		slices.SortFunc(group.Pods, func(a, b *corev1.Pod) int {
			return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name), cmp.Compare(a.UID, b.UID))
		})
		result = append(result, *group)
	}
	slices.SortFunc(result, func(a, b SchedulingGroup) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), slices.Compare(a.Keys, b.Keys))
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
