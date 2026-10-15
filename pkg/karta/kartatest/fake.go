// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package kartatest provides a hand-rolled recording fake of karta.Workload
// for consumer tests. No cluster, no fixtures, no mock generation.
package kartatest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/pkg/tree"
)

// Fake is a RECORDING fake of karta.Workload for consumer unit tests: calls
// are recorded, and every update is validated through
// karta.ValidatePodTemplateUpdate - the exact code path production runs, so
// the fake cannot drift from it. It does not run any jq and does not mutate
// objects; for integration-grade tests use karta.New over an in-memory
// object, which needs no cluster.
type Fake struct {
	componentInfos []karta.ComponentInfo
	definitions    map[string]v1alpha1.ComponentDefinition
	suspendable    bool

	// Recorded calls. Typed patches are recorded in their compiled
	// merge-patch form - the same thing production routes.
	PodUpdates []PodUpdate
	Suspends   int
	Resumes    int
}

// PodUpdate is one recorded UpdatePodTemplate call.
type PodUpdate struct {
	Component string
	Patch     karta.Patch
	Instances []string
}

var _ karta.Workload = (*Fake)(nil)

// NewFromKarta builds a fake whose capability behavior comes from a real
// Karta definition, exactly like production.
func NewFromKarta(definition *v1alpha1.Karta) *Fake {
	definition = definition.DeepCopy()
	fake := &Fake{definitions: map[string]v1alpha1.ComponentDefinition{}}
	fake.componentInfos = karta.ComponentInfos(definition)
	remember := func(def v1alpha1.ComponentDefinition) {
		fake.definitions[def.Name] = def
		if def.SuspendDefinition != nil {
			fake.suspendable = true
		}
	}
	remember(definition.Spec.StructureDefinition.RootComponent)
	for _, child := range definition.Spec.StructureDefinition.ChildComponents {
		remember(child)
	}
	return fake
}

func (f *Fake) Tree(_ context.Context) (*tree.WorkloadTree, error) {
	return &tree.WorkloadTree{}, nil
}

func (f *Fake) Components() []karta.ComponentInfo {
	infos := slices.Clone(f.componentInfos)
	for i := range infos {
		infos[i].PodFields = slices.Clone(infos[i].PodFields)
	}
	return infos
}

func (f *Fake) UpdatePodTemplate(_ context.Context, component string, update karta.PodTemplateUpdate, opts ...karta.UpdateOption) error {
	definition, ok := f.definitions[component]
	if !ok {
		return fmt.Errorf("karta: component %s not found", component)
	}
	if err := karta.ValidatePodTemplateUpdate(definition, component, update); err != nil {
		return err
	}
	patch := update.AsPodMergePatch()
	encoded, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("karta: encode recorded patch: %w", err)
	}
	var recorded karta.Patch
	if err := json.Unmarshal(encoded, &recorded); err != nil {
		return fmt.Errorf("karta: decode recorded patch: %w", err)
	}
	f.PodUpdates = append(f.PodUpdates, PodUpdate{
		Component: component,
		Patch:     recorded,
		Instances: slices.Clone(karta.ResolveUpdateOptions(opts...).Instances),
	})
	return nil
}

func (f *Fake) Suspend(_ context.Context) error {
	if !f.suspendable {
		return &karta.UnsupportedOperationError{Op: "suspend"}
	}
	f.Suspends++
	return nil
}

func (f *Fake) Resume(_ context.Context) error {
	if !f.suspendable {
		return &karta.UnsupportedOperationError{Op: "resume"}
	}
	f.Resumes++
	return nil
}

func (f *Fake) Object() (resource.KubernetesObject, error) {
	return nil, nil
}
