// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package tree

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

// WorkloadTree is the raw tree produced by Build().
// It holds the extracted root values, the child hierarchy and normalized status.
type WorkloadTree struct {
	// Root holds the root component and its instance values. Children below carry
	// the hierarchy. The serialized tree keeps its existing status and children shape.
	Root *ComponentNode `json:"-"`
	// Status is nil when no status was evaluated, e.g. an offline pre-submission
	// tree built from a manifest that has not reached the cluster.
	Status   *WorkloadStatus
	Children []ComponentNode
}

// WorkloadStatus holds the normalized statuses extracted from the root component.
type WorkloadStatus struct {
	// Phases holds all ResourceStatus values that matched the workload's StatusMappings.
	// Status mappings are not mutually exclusive, so there can be multiple matches
	// (e.g. ["Running", "Degraded"]). Empty means no match (Undefined).
	Phases []string
}

// ComponentNode represents one component in the hierarchy.
//
// Every component always carries at least one InstanceNode, even a
// single-instance component (one without an instanceIds accessor), which has exactly
// one InstanceNode with a nil InstanceKey. The shape is deliberately uniform -
// component -> instance -> component - so consumers can walk the tree without
// special-casing single- versus multi-instance components.
type ComponentNode struct {
	Name string
	// Kind is nil for logical grouping components (those without a Kind in the definition).
	Kind             *metav1.GroupVersionKind
	HasPodDefinition bool
	Instances        []InstanceNode
}

// InstanceNode represents one instance of a component.
// InstanceKey is nil for single-instance components (no instanceIds accessor defined).
// ReplicaKey is nil when no ReplicaSelector is defined on the component.
// Both axes are orthogonal and can co-exist.
type InstanceNode struct {
	InstanceKey       *string
	ReplicaKey        *string
	Scale             *resource.Scale
	ExtractedInstance *resource.ExtractedInstance
	Children          []ComponentNode
}
