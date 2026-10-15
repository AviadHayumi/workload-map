// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package workloadinterface demonstrates consumer-owned interfaces for desired
// workload inspection and observed Pod metrics using the public Karta SDK.
package workloadinterface

import (
	"context"
	"fmt"
	"slices"

	"github.com/dsx-ai-factory/workload-map/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/pkg/tree"
)

// DesiredInspector is the inspection capability used by this consumer.
type DesiredInspector interface {
	Tree(context.Context) (*tree.WorkloadTree, error)
}

// DesiredSummary retains the public extraction model, including raw status and
// conditions. It describes desired structure, not observed Pod membership.
type DesiredSummary struct {
	Instances []DesiredInstance
	Status    *resource.Status
	Phases    []string
}

// DesiredInstance describes one declared component instance. Extracted retains
// complete templates, split PodSpec/Metadata, and fragmented specs without
// pretending those shapes are interchangeable.
type DesiredInstance struct {
	Component   string
	Path        []string
	InstanceKey *string
	ReplicaKey  *string
	Scale       *resource.Scale
	Extracted   *resource.ExtractedInstance
}

// SummarizeDesired walks the root and every declared child instance. The result
// retains data from the inspector's returned snapshot.
func SummarizeDesired(ctx context.Context, inspector DesiredInspector) (*DesiredSummary, error) {
	workloadTree, err := inspector.Tree(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect desired workload: %w", err)
	}
	if workloadTree == nil || workloadTree.Root == nil {
		return nil, fmt.Errorf("inspect desired workload: root is missing")
	}
	summary := &DesiredSummary{Status: workloadTree.RootStatus}
	if workloadTree.Status != nil {
		summary.Phases = slices.Clone(workloadTree.Status.Phases)
	}
	var visit func(*tree.ComponentNode, []string)
	visit = func(component *tree.ComponentNode, parentPath []string) {
		path := append(slices.Clone(parentPath), component.Name)
		for _, instance := range component.Instances {
			summary.Instances = append(summary.Instances, DesiredInstance{
				Component: component.Name, Path: slices.Clone(path),
				InstanceKey: instance.InstanceKey, ReplicaKey: instance.ReplicaKey,
				Scale: instance.Scale, Extracted: instance.ExtractedInstance,
			})
			for i := range instance.Children {
				visit(&instance.Children[i], path)
			}
		}
	}
	visit(workloadTree.Root, nil)
	return summary, nil
}
