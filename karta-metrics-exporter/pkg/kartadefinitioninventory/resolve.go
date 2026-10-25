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

// sourceFilter selects by include name OR label selector, everything when both
// are empty; exclude wins.
type sourceFilter struct {
	enabled  bool
	include  sets.Set[string]
	exclude  sets.Set[string]
	selector labels.Selector
}

// resolve does not validate: an invalid winner still shadows what it outranked.
func resolve(catalogDefinitions, clusterDefinitions []*v1alpha1.Karta, catalogFilter, clusterFilter sourceFilter) snapshot {
	s := snapshot{selections: map[schema.GroupKind]candidate{}}
	// The SDK refuses to build a catalog with an unkeyable definition.
	fromCatalog, _ := admit(catalogDefinitions, catalogFilter)
	fromCluster, unkeyable := admit(clusterDefinitions, clusterFilter)
	s.unkeyable = unkeyable

	if clusterFilter.enabled {
		names := sets.New[string]()
		for _, k := range clusterDefinitions {
			names.Insert(k.Name)
		}
		s.unmatchedIncludes = sets.List(clusterFilter.include.Difference(names))
	}

	// Both orders end on the name, so each winner is independent of input order.
	for gk, candidates := range fromCatalog {
		// Highest Kubernetes version priority first, as the API server prefers.
		winner := slices.MinFunc(candidates, func(a, b *v1alpha1.Karta) int {
			return cmp.Or(
				version.CompareKubeAwareVersionStrings(catalog.RootKey(b).Version, catalog.RootKey(a).Version),
				strings.Compare(a.Name, b.Name))
		})
		s.selections[gk] = newCandidate(winner, SourceCatalog, validateDefinition)
	}
	// A selected cluster definition replaces the catalog one for its group and kind.
	for gk, candidates := range fromCluster {
		winner := slices.MinFunc(candidates, func(a, b *v1alpha1.Karta) int {
			return cmp.Or(
				a.CreationTimestamp.Compare(b.CreationTimestamp.Time),
				strings.Compare(a.Name, b.Name))
		})
		s.selections[gk] = newCandidate(winner, SourceCluster, validateDefinition)
	}
	return s
}

func admit(definitions []*v1alpha1.Karta, filter sourceFilter) (map[schema.GroupKind][]*v1alpha1.Karta, []string) {
	byKind := map[schema.GroupKind][]*v1alpha1.Karta{}
	if !filter.enabled {
		return byKind, nil
	}
	hasSelector := filter.selector != nil && !filter.selector.Empty()
	selectsAll := filter.include.Len() == 0 && !hasSelector
	var unkeyable []string
	for _, k := range definitions {
		selected := selectsAll || filter.include.Has(k.Name) || (hasSelector && filter.selector.Matches(labels.Set(k.Labels)))
		if !selected || filter.exclude.Has(k.Name) {
			continue
		}
		gvk := catalog.RootKey(k)
		if gvk.Version == "" || gvk.Kind == "" {
			unkeyable = append(unkeyable, k.Name)
			continue
		}
		byKind[gvk.GroupKind()] = append(byKind[gvk.GroupKind()], k)
	}
	slices.Sort(unkeyable)
	return byKind, unkeyable
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
	c, ok := s.selections[gvk.GroupKind()]
	if !ok {
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
	out := make([]Selection, 0, len(s.selections))
	var errs []error
	kinds := slices.SortedFunc(maps.Keys(s.selections), func(a, b schema.GroupKind) int {
		return strings.Compare(a.String(), b.String())
	})
	for _, gk := range kinds {
		c := s.selections[gk]
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
