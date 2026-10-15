// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package attribution evaluates logical Pod membership independently of object
// acquisition, desired workload structure, and exporter metric projection.
package attribution

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

// Plan owns a validated, immutable definition and its component ancestry.
type Plan struct {
	definition  *v1alpha1.Karta
	definitions map[string]*v1alpha1.ComponentDefinition
	producers   []string
	paths       map[string][]string
}

// Prepared contains only instance declarations extracted from one root object.
// Component extraction errors are retained without preventing other components
// from being evaluated. It retains no reference to the input object.
type Prepared struct {
	plan     *Plan
	ids      map[string][]string
	failures map[string]preparationFailure
}

// A value error keeps preparation diagnostics immutable when returned to callers.
type preparationFailure string

func (e preparationFailure) Error() string { return string(e) }

// Compile validates and copies a definition. No workload expressions are run.
func Compile(k *v1alpha1.Karta) (*Plan, error) {
	if k == nil {
		return nil, fmt.Errorf("karta is nil")
	}
	definition := k.DeepCopy()
	if err := v1alpha1.NewKartaValidator(definition).Validate(); err != nil {
		return nil, fmt.Errorf("invalid karta: %w", err)
	}
	p := &Plan{
		definition:  definition,
		definitions: make(map[string]*v1alpha1.ComponentDefinition),
		paths:       make(map[string][]string),
	}
	all := append([]v1alpha1.ComponentDefinition{definition.Spec.StructureDefinition.RootComponent}, definition.Spec.StructureDefinition.ChildComponents...)
	for i := range all {
		component := &all[i]
		p.definitions[component.Name] = component
		spec := component.SpecDefinition
		if spec != nil && (spec.PodSpecPath != nil || spec.PodTemplateSpecPath != nil || spec.FragmentedPodSpecDefinition != nil) {
			p.producers = append(p.producers, component.Name)
		}
	}
	for _, component := range all {
		var path []string
		for current := component.Name; current != ""; {
			path = append(path, current)
			parent := p.definitions[current].OwnerRef
			if parent == nil {
				break
			}
			current = *parent
		}
		slices.Reverse(path)
		p.paths[component.Name] = path
	}
	return p, nil
}

// Definition returns a caller-owned copy of the compiled definition.
func (p *Plan) Definition() *v1alpha1.Karta {
	if p == nil {
		return nil
	}
	return p.definition.DeepCopy()
}

// Prepare extracts instance IDs for every component, including logical ancestors.
// It does not extract templates, scale, status, or a desired tree.
func Prepare(ctx context.Context, p *Plan, root resource.KubernetesObject) (*Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.definition == nil {
		return nil, fmt.Errorf("attribution plan is nil")
	}
	ids, failures, err := extractInstanceIDs(ctx, p.definition, root, true)
	if err != nil {
		return nil, err
	}
	immutableFailures := make(map[string]preparationFailure, len(failures))
	for component, failure := range failures {
		immutableFailures[component] = preparationFailure(failure.Error())
	}
	return &Prepared{plan: p, ids: ids, failures: immutableFailures}, nil
}

// Plan returns the immutable plan used to prepare this observation.
func (p *Prepared) Plan() *Plan {
	if p == nil {
		return nil
	}
	return p.plan
}

// InstanceIDs returns a caller-owned slice, or the component's preparation error.
// The empty string denotes a declared anonymous instance.
func (p *Prepared) InstanceIDs(component string) ([]string, error) {
	if p == nil || p.plan == nil {
		return nil, fmt.Errorf("prepared attribution is nil")
	}
	if _, ok := p.plan.definitions[component]; !ok {
		return nil, fmt.Errorf("component %q not found", component)
	}
	if err, ok := p.failures[component]; ok {
		return nil, err
	}
	return slices.Clone(p.ids[component]), nil
}

// InstanceIDs extracts declared instance IDs from a root object. With
// allComponents false, only Pod-producing components are included, preserving
// the exporter's once-per-workload preparation behavior.
func InstanceIDs(ctx context.Context, k *v1alpha1.Karta, root resource.KubernetesObject, allComponents bool) (map[string][]string, error) {
	ids, failures, err := extractInstanceIDs(ctx, k, root, allComponents)
	if err != nil {
		return nil, err
	}
	// Match the factory's stable children-then-root error order.
	components := append(slices.Clone(k.Spec.StructureDefinition.ChildComponents), k.Spec.StructureDefinition.RootComponent)
	for _, component := range components {
		if failure, ok := failures[component.Name]; ok {
			return nil, failure
		}
	}
	return ids, nil
}

func extractInstanceIDs(ctx context.Context, k *v1alpha1.Karta, root resource.KubernetesObject, allComponents bool) (map[string][]string, map[string]error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if k == nil {
		return nil, nil, fmt.Errorf("karta is nil")
	}
	if root == nil || (reflect.ValueOf(root).Kind() == reflect.Pointer && reflect.ValueOf(root).IsNil()) {
		return nil, nil, fmt.Errorf("root object is nil")
	}
	factory := resource.NewComponentFactoryFromObject(k, root)
	components, err := factory.GetChildComponents()
	if err != nil {
		return nil, nil, err
	}
	rootComponent, err := factory.GetRootComponent()
	if err != nil {
		return nil, nil, err
	}
	components = append(components, rootComponent)
	ids := make(map[string][]string, len(components))
	failures := make(map[string]error)
	for _, component := range components {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !allComponents && !component.HasPodDefinition() {
			continue
		}
		values, err := component.GetInstanceIds(ctx)
		if cancellation := ctx.Err(); cancellation != nil {
			return nil, nil, cancellation
		}
		if err != nil {
			failures[component.Name()] = err
			continue
		}
		ids[component.Name()] = slices.Clone(values)
	}
	return ids, failures, nil
}
