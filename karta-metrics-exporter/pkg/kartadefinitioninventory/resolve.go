// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/version"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

// snapshot references its inputs and is never modified; reads return copies.
type snapshot struct {
	selections        map[schema.GroupKind]candidate
	unmatchedIncludes []string
	// unkeyable lists selected cluster definitions whose root has no version or kind.
	unkeyable []string
}

// candidate validates lazily, at most once.
type candidate struct {
	selection Selection
	validate  func() error
}

// sourceFilter selects a definition when its name is included and its labels
// match; an empty list or selector allows all, and exclude wins.
type sourceFilter struct {
	enabled  bool
	include  sets.Set[string]
	exclude  sets.Set[string]
	selector labels.Selector
}

// resolve does not validate: an invalid winner still shadows what it outranked.
func resolve(catalogDefinitions, clusterDefinitions []*v1alpha1.Karta, catalogFilter, clusterFilter sourceFilter) snapshot {
	// The SDK refuses to build a catalog with an unkeyable definition.
	fromCatalog, _ := selectDefinitions(catalogDefinitions, catalogFilter, SourceCatalog)
	fromCluster, unkeyable := selectDefinitions(clusterDefinitions, clusterFilter, SourceCluster)
	// A selected cluster definition replaces the catalog one for its group and kind.
	maps.Copy(fromCatalog, fromCluster)
	s := snapshot{selections: fromCatalog, unkeyable: unkeyable}

	if clusterFilter.enabled {
		names := sets.New[string]()
		for _, k := range clusterDefinitions {
			names.Insert(k.Name)
		}
		s.unmatchedIncludes = sets.List(clusterFilter.include.Difference(names))
	}

	return s
}

func selectDefinitions(definitions []*v1alpha1.Karta, filter sourceFilter, source Source) (map[schema.GroupKind]candidate, []string) {
	selected := map[schema.GroupKind]candidate{}
	var unkeyable []string
	for _, k := range definitions {
		if !filter.matches(k) {
			continue
		}
		gvk := catalog.RootKey(k)
		if gvk.Version == "" || gvk.Kind == "" {
			unkeyable = append(unkeyable, k.Name)
			continue
		}
		c := newCandidate(k, source, validateDefinition)
		if c.preferredTo(selected[gvk.GroupKind()]) {
			selected[gvk.GroupKind()] = c
		}
	}
	slices.Sort(unkeyable)
	return selected, unkeyable
}

func (f sourceFilter) matches(k *v1alpha1.Karta) bool {
	return f.enabled && (f.include.Len() == 0 || f.include.Has(k.Name)) &&
		!f.exclude.Has(k.Name) && (f.selector == nil || f.selector.Matches(labels.Set(k.Labels)))
}

func (c candidate) preferredTo(other candidate) bool {
	if other.selection.Definition == nil {
		return true
	}
	if c.selection.Origin.Source != other.selection.Origin.Source {
		return c.selection.Origin.Source == SourceCluster
	}
	a, b := c.selection.Definition, other.selection.Definition
	var order int
	if c.selection.Origin.Source == SourceCluster {
		order = a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
	} else {
		order = version.CompareKubeAwareVersionStrings(other.selection.GVK.Version, c.selection.GVK.Version)
	}
	return cmp.Or(order, strings.Compare(a.Name, b.Name)) < 0
}

func newCandidate(k *v1alpha1.Karta, source Source, validate func(*v1alpha1.Karta) error) candidate {
	sel := Selection{Definition: k, GVK: catalog.RootKey(k), Origin: Origin{Source: source, Name: k.Name}}
	if source == SourceCluster {
		sel.Origin.UID = k.UID
		sel.Origin.ResourceVersion = k.ResourceVersion
	}
	return candidate{selection: sel, validate: sync.OnceValue(func() error { return validate(k) })}
}

func validateDefinition(k *v1alpha1.Karta) error {
	c := k.DeepCopy()
	// The SDK requires a root status definition; the exporter does not.
	if c.Spec.StructureDefinition.RootComponent.StatusDefinition == nil {
		c.Spec.StructureDefinition.RootComponent.StatusDefinition = &v1alpha1.StatusDefinition{}
	}
	return v1alpha1.NewKartaValidator(c).Validate()
}

// lookup ignores the requested version and never falls back from an invalid winner.
func (s snapshot) lookup(gvk schema.GroupVersionKind) (Selection, error) {
	return s.selections[gvk.GroupKind()].lookup()
}

func (c candidate) lookup() (Selection, error) {
	if c.selection.Definition == nil {
		return Selection{}, nil
	}
	if err := c.validate(); err != nil {
		return Selection{}, fmt.Errorf("%w %q: %w", ErrInvalidDefinition, c.selection.Origin.Name, err)
	}
	return c.copy(), nil
}

// elected skips validation.
func (s snapshot) elected(gvk schema.GroupVersionKind) (Selection, bool) {
	c, ok := s.selections[gvk.GroupKind()]
	if !ok {
		return Selection{}, false
	}
	return c.copy(), true
}

// list keeps invalid winners: watch setup still needs their kinds.
func (s snapshot) list() ([]Selection, error) {
	return listCandidates(s.selections)
}

func listCandidates(candidates map[schema.GroupKind]candidate) ([]Selection, error) {
	out := make([]Selection, 0, len(candidates))
	var errs []error
	kinds := slices.SortedFunc(maps.Keys(candidates), func(a, b schema.GroupKind) int {
		return strings.Compare(a.String(), b.String())
	})
	for _, gk := range kinds {
		c := candidates[gk]
		if err := c.validate(); err != nil {
			errs = append(errs, fmt.Errorf("%w %q: %w", ErrInvalidDefinition, c.selection.Origin.Name, err))
		}
		out = append(out, c.copy())
	}
	return out, errors.Join(errs...)
}

func (c candidate) copy() Selection {
	sel := c.selection
	sel.Definition = sel.Definition.DeepCopy()
	return sel
}
