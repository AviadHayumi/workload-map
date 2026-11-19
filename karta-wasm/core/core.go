// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package core provides Karta operations shared by the browser and WASI doors,
// keeping both transports as thin input and output adapters.
package core

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	jsonutil "k8s.io/apimachinery/pkg/util/json"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/tree"
)

// DecodeDefinition parses a Karta definition from JSON.
func DecodeDefinition(definitionJSON string) (*api.Karta, error) {
	var definition api.Karta
	if err := json.Unmarshal([]byte(definitionJSON), &definition); err != nil {
		return nil, fmt.Errorf("failed to unmarshal definition: %w", err)
	}
	return &definition, nil
}

// DecodeWorkload parses a workload from JSON into an unstructured object, so
// fields from any Kubernetes resource type survive.
func DecodeWorkload(workloadJSON string) (*unstructured.Unstructured, error) {
	var workload map[string]any
	if err := jsonutil.Unmarshal([]byte(workloadJSON), &workload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workload: %w", err)
	}
	return &unstructured.Unstructured{Object: workload}, nil
}

// ReferenceQuery is one eligible Kubernetes request planned by the SDK.
type ReferenceQuery struct {
	Binding    string `json:"binding"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name,omitempty"`
	Selector   string `json:"selector,omitempty"`
}

// ReferenceQueries runs after a read asks for references. CEL always runs in Go.
func ReferenceQueries(ctx context.Context, definitionJSON, workloadJSON string) ([]ReferenceQuery, error) {
	definition, err := DecodeDefinition(definitionJSON)
	if err != nil {
		return nil, err
	}
	workload, err := DecodeWorkload(workloadJSON)
	if err != nil {
		return nil, err
	}
	if err := api.NewKartaValidator(definition).Validate(); err != nil {
		return nil, err
	}
	programs, err := api.ProgramsFor(definition)
	if err != nil {
		return nil, err
	}
	queries := []ReferenceQuery{}
	for _, ref := range definition.Spec.StructureDefinition.References {
		q, err := references.Plan(ctx, ref, workload.Object, cel.WithPrograms(programs))
		if err != nil {
			return nil, err
		}
		if !q.Active {
			continue
		}
		query := ReferenceQuery{Binding: ref.Name, APIVersion: q.GVK.GroupVersion().String(), Kind: q.GVK.Kind, Namespace: q.Namespace, Name: q.Name}
		if q.Selector != nil {
			query.Selector = q.Selector.String()
		}
		queries = append(queries, query)
	}
	return queries, nil
}

// ReadOptions is the browser's observation. An empty supplied map is a valid snapshot.
type ReadOptions struct {
	ReferencesEnabled *bool                         `json:"referencesEnabled"`
	References        references.ResolvedReferences `json:"references"`
}

func observation(definitionJSON, workloadJSON string, opts []resource.FactoryOption) (*resource.ComponentFactory, error) {
	definition, err := DecodeDefinition(definitionJSON)
	if err != nil {
		return nil, err
	}
	workload, err := DecodeWorkload(workloadJSON)
	if err != nil {
		return nil, err
	}
	return resource.NewComponentFactoryFromObject(definition, workload, opts...), nil
}

// BuildTree builds the workload tree, including the root status.
func BuildTree(ctx context.Context, definitionJSON, workloadJSON string, opts ...resource.FactoryOption) (*tree.WorkloadTree, error) {
	factory, err := observation(definitionJSON, workloadJSON, opts)
	if err != nil {
		return nil, err
	}
	return tree.Build(ctx, factory)
}

func rootComponent(definitionJSON, workloadJSON string, opts []resource.FactoryOption) (*resource.Component, error) {
	factory, err := observation(definitionJSON, workloadJSON, opts)
	if err != nil {
		return nil, err
	}
	if err := api.NewKartaValidator(factory.GetKarta()).Validate(); err != nil {
		return nil, err
	}
	return factory.GetRootComponent()
}

// RootInstances exposes the workload's own mapped values without extracting its children.
func RootInstances(ctx context.Context, definitionJSON, workloadJSON string, opts ...resource.FactoryOption) (map[string]resource.ExtractedInstance, error) {
	root, err := rootComponent(definitionJSON, workloadJSON, opts)
	if err != nil {
		return nil, err
	}
	return root.GetExtractedInstances(ctx)
}

// EvaluatePhases reads just the root status. References used only by component
// fields do not require a Kubernetes request to render the workloads table.
func EvaluatePhases(ctx context.Context, definitionJSON, workloadJSON string, opts ...resource.FactoryOption) ([]string, error) {
	factory, err := observation(definitionJSON, workloadJSON, opts)
	if err != nil {
		return nil, err
	}
	status, err := tree.ReadStatus(ctx, factory)
	if err != nil {
		return nil, err
	}
	if status == nil {
		return []string{}, nil
	}
	return status.Phases, nil
}

// ListCatalog returns the Karta definitions embedded at build time.
func ListCatalog() []*api.Karta {
	return catalog.List()
}
