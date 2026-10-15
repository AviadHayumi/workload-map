// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package tree

import (
	"iter"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

// WorkloadTree contains the desired hierarchy and extracted data from Build.
type WorkloadTree struct {
	// Root includes the root component's instances, templates, scale and children.
	Root *ComponentNode
	// RootStatus preserves the raw phase, conditions and normalized statuses.
	RootStatus *resource.Status
	// Status projects the normalized phases from RootStatus.
	Status *WorkloadStatus
	// Children exposes the first root instance's children for compatibility.
	// It excludes Root, including when Root has a pod definition.
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
// single-instance component (one without an InstanceIdPath), which has exactly
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
// InstanceKey is nil for single-instance components (no InstanceIdPath defined).
// ReplicaKey is nil when no ReplicaSelector is defined on the component.
// Both axes are orthogonal and can co-exist.
type InstanceNode struct {
	InstanceKey       *string
	ReplicaKey        *string
	Scale             *resource.Scale
	ExtractedInstance *resource.ExtractedInstance
	Children          []ComponentNode
}

// Instances yields every child component and instance pair depth-first.
// It traverses Children, including nested descendants, and excludes Root.
func (t *WorkloadTree) Instances() iter.Seq2[ComponentNode, InstanceNode] {
	return func(yield func(ComponentNode, InstanceNode) bool) {
		walkInstances(t.Children, yield)
	}
}

func walkInstances(nodes []ComponentNode, yield func(ComponentNode, InstanceNode) bool) bool {
	for _, component := range nodes {
		for _, instance := range component.Instances {
			if !yield(component, instance) {
				return false
			}
			if !walkInstances(instance.Children, yield) {
				return false
			}
		}
	}
	return true
}
