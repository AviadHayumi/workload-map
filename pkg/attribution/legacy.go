// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package attribution

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

// LegacyResult preserves exporter attribution's field degradation and reason strings.
type LegacyResult struct {
	Component string
	Instance  string
	Replica   string
	Reason    string
}

// These values form the existing exporter metric contract.
const (
	LegacyUnknown         = "<unknown>"
	LegacyUnknownInstance = "unknown_instance"
	LegacyJQError         = "jq_error"
)

// AttributeLegacy resolves which component, component instance, and replica of a
// workload the given pod belongs to. All jq runs against the pod only: the
// workload side is the instanceIDs map (component name to declared instance
// ids), which the caller computes once per workload event, not per pod.
func AttributeLegacy(ctx context.Context, pod *corev1.Pod, summary *instructions.StructureSummary, definitions map[string]*v1alpha1.ComponentDefinition, instanceIDs map[string][]string) LegacyResult {
	querier := resource.NewPodQuerier(pod)

	componentName, err := instructions.InferPodComponent(ctx, querier, summary)
	if err != nil {
		return LegacyResult{
			Component: LegacyUnknown,
			Instance:  LegacyUnknown,
			Reason:    LegacyJQError,
		}
	}

	result := LegacyResult{Component: componentName}
	definition := definitions[componentName]
	if definition == nil {
		result.Instance = LegacyUnknown
		result.Reason = LegacyUnknownInstance
		return result
	}

	ids := instanceIDs[componentName]
	if len(ids) == 0 && definition.PodSelector != nil && definition.PodSelector.ComponentInstanceSelector != nil {
		result.Instance = LegacyUnknown
		result.Reason = LegacyUnknownInstance
		return result
	}
	if len(ids) > 0 && ids[0] != "" && definition.PodSelector != nil {
		instance, err := querier.GetMatchingInstanceId(ctx, definition.PodSelector.ComponentInstanceSelector, ids)
		switch {
		case err == nil:
			result.Instance = instance
		case errors.As(err, new(resource.InstanceNotFoundError)):
			result.Instance = LegacyUnknown
			result.Reason = LegacyUnknownInstance
		default:
			result.Instance = LegacyUnknown
			result.Reason = LegacyJQError
		}
	}

	replica, err := extractReplica(ctx, querier, definitions, componentName)
	if err != nil && result.Reason == "" {
		result.Reason = LegacyJQError
	}
	result.Replica = replica

	return result
}

// extractReplica finds the nearest ReplicaSelector on the component or its
// ancestors (descendants inherit the replica context from the ancestor that
// defines it) and evaluates it against the pod.
func extractReplica(ctx context.Context, querier *resource.PodQuerier, definitions map[string]*v1alpha1.ComponentDefinition, componentName string) (string, error) {
	current := componentName
	for current != "" {
		definition := definitions[current]
		if definition == nil {
			return "", nil
		}

		if selector := definition.PodSelector; selector != nil && selector.ReplicaSelector != nil {
			replica, found, err := querier.ExtractReplicaKey(ctx, selector.ReplicaSelector)
			if err != nil {
				return "", err
			}
			if found {
				return replica, nil
			}
			return "", nil
		}

		if definition.OwnerRef == nil {
			return "", nil
		}
		current = *definition.OwnerRef
	}
	return "", nil
}
