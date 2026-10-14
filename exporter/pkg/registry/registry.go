// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package registry

import (
	"fmt"
	"sort"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/instructions"
)

// Entry is a validated Karta chosen to serve its (group, kind).
type Entry struct {
	Catalog     bool
	Karta       *v1alpha1.Karta
	RootGVK     schema.GroupVersionKind
	Summary     *instructions.StructureSummary
	ChildKinds  []schema.GroupVersionKind
	Definitions map[string]*v1alpha1.ComponentDefinition
}

// Stats counts Kartas per validity and reason, for self-observability.
type Stats struct {
	Valid    int
	Invalid  int
	Shadowed int
}

type kartaState struct {
	karta   *v1alpha1.Karta
	err     error
	rootGK  schema.GroupKind
	catalog bool
}

// Registry tracks cluster Karta CRs, validates them, and picks exactly one
// Karta per root (group, kind) so one set of objects is never watched twice.
// The pick is deterministic: oldest creationTimestamp, then name.
type Registry struct {
	mu     sync.RWMutex
	kartas map[string]kartaState
	chosen map[schema.GroupKind]*Entry
}

func New() *Registry {
	return &Registry{
		kartas: make(map[string]kartaState),
		chosen: make(map[schema.GroupKind]*Entry),
	}
}

// SeedCatalog loads the built-in catalog definitions as a fallback tier:
// a cluster Karta CR for the same group and kind always overrides them.
func (r *Registry) SeedCatalog() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, karta := range catalog.List() {
		rootKind := karta.Spec.StructureDefinition.RootComponent.Kind
		if rootKind == nil {
			continue
		}
		// the bare-Pod catalog entry would turn every pod on the cluster
		// into a workload; it stays opt-in via an explicit Karta CR
		if rootKind.Group == "" && rootKind.Kind == "Pod" {
			continue
		}
		r.kartas["catalog:"+karta.Name] = kartaState{
			karta:   karta,
			rootGK:  schema.GroupKind{Group: rootKind.Group, Kind: rootKind.Kind},
			catalog: true,
		}
	}
	r.recomputeLocked()
}

// Set adds or updates a Karta and recomputes the chosen entries.
func (r *Registry) Set(karta *v1alpha1.Karta) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := kartaState{karta: karta.DeepCopy()}
	karta = state.karta
	if rootKind := karta.Spec.StructureDefinition.RootComponent.Kind; rootKind != nil {
		state.rootGK = schema.GroupKind{Group: rootKind.Group, Kind: rootKind.Kind}
	}
	if err := v1alpha1.NewKartaValidator(karta).Validate(); err != nil {
		state.err = fmt.Errorf("invalid karta: %w", err)
	} else if karta.Spec.StructureDefinition.RootComponent.Kind == nil {
		state.err = fmt.Errorf("root component has no kind")
	} else {
		rootKind := karta.Spec.StructureDefinition.RootComponent.Kind
		state.rootGK = schema.GroupKind{Group: rootKind.Group, Kind: rootKind.Kind}
	}

	r.kartas[karta.Name] = state
	r.recomputeLocked()
}

// Remove deletes a Karta by name and recomputes the chosen entries.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.kartas, name)
	r.recomputeLocked()
}

// EntryFor returns the chosen entry serving the given root group and kind.
func (r *Registry) EntryFor(groupKind schema.GroupKind) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.chosen[groupKind]
	return entry, ok
}

// Entries returns all chosen entries.
func (r *Registry) Entries() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := make([]*Entry, 0, len(r.chosen))
	for _, entry := range r.chosen {
		entries = append(entries, entry)
	}
	return entries
}

// IsRoot reports whether a group and kind belongs to a chosen entry.
func (r *Registry) IsRoot(groupKind schema.GroupKind) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.chosen[groupKind]
	return ok
}

// Stats returns Karta counts for the self-observability gauge.
func (r *Registry) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stats := Stats{}
	for _, state := range r.kartas {
		chosen := r.chosen[state.rootGK]
		switch {
		case state.err != nil:
			stats.Invalid++
		case chosen != nil && chosen.Karta == state.karta:
			stats.Valid++
		case !state.catalog:
			// a catalog entry overridden by a CR is expected, not shadowed
			stats.Shadowed++
		}
	}
	return stats
}

func (r *Registry) recomputeLocked() {
	crCandidates := make(map[schema.GroupKind][]*v1alpha1.Karta)
	catalogCandidates := make(map[schema.GroupKind][]*v1alpha1.Karta)
	for _, state := range r.kartas {
		if state.err != nil {
			continue
		}
		if state.catalog {
			catalogCandidates[state.rootGK] = append(catalogCandidates[state.rootGK], state.karta)
		} else {
			crCandidates[state.rootGK] = append(crCandidates[state.rootGK], state.karta)
		}
	}

	chosen := make(map[schema.GroupKind]*Entry)
	for groupKind, kartas := range crCandidates {
		sort.Slice(kartas, func(i, j int) bool {
			iTime, jTime := kartas[i].CreationTimestamp, kartas[j].CreationTimestamp
			if !iTime.Equal(&jTime) {
				return iTime.Before(&jTime)
			}
			return kartas[i].Name < kartas[j].Name
		})

		entry, err := newEntry(kartas[0])
		if err != nil {
			continue
		}
		chosen[groupKind] = entry
	}

	for groupKind, kartas := range catalogCandidates {
		if _, ok := chosen[groupKind]; ok {
			continue
		}
		// Catalog definitions use the same deterministic tie rule as CRs.
		sort.Slice(kartas, func(i, j int) bool {
			iTime, jTime := kartas[i].CreationTimestamp, kartas[j].CreationTimestamp
			if !iTime.Equal(&jTime) {
				return iTime.Before(&jTime)
			}
			return kartas[i].Name < kartas[j].Name
		})

		entry, err := newEntry(kartas[0])
		if err != nil {
			continue
		}
		entry.Catalog = true
		chosen[groupKind] = entry
	}
	r.chosen = chosen
}

func newEntry(karta *v1alpha1.Karta) (*Entry, error) {
	summary, err := instructions.NewStructureSummary(karta)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize karta %s: %w", karta.Name, err)
	}

	rootKind := karta.Spec.StructureDefinition.RootComponent.Kind
	entry := &Entry{
		Karta: karta,
		RootGVK: schema.GroupVersionKind{
			Group:   rootKind.Group,
			Version: rootKind.Version,
			Kind:    rootKind.Kind,
		},
		Summary:     summary,
		Definitions: map[string]*v1alpha1.ComponentDefinition{},
	}
	root := &karta.Spec.StructureDefinition.RootComponent
	entry.Definitions[root.Name] = root
	for index := range karta.Spec.StructureDefinition.ChildComponents {
		child := &karta.Spec.StructureDefinition.ChildComponents[index]
		entry.Definitions[child.Name] = child
	}

	seen := map[schema.GroupVersionKind]struct{}{}
	addChildKind := func(gvk schema.GroupVersionKind) {
		if _, ok := seen[gvk]; ok {
			return
		}
		seen[gvk] = struct{}{}
		entry.ChildKinds = append(entry.ChildKinds, gvk)
	}
	for _, child := range karta.Spec.StructureDefinition.ChildComponents {
		if child.Kind == nil {
			continue
		}
		addChildKind(schema.GroupVersionKind{Group: child.Kind.Group, Version: child.Kind.Version, Kind: child.Kind.Kind})
	}
	for _, kind := range karta.Spec.StructureDefinition.AdditionalChildKinds {
		addChildKind(schema.GroupVersionKind{Group: kind.Group, Version: kind.Version, Kind: kind.Kind})
	}

	return entry, nil
}

// InvalidKinds identifies only definitions whose target kind cannot be served.
// Invalid definitions do not poison unrelated valid definitions.
func (r *Registry) InvalidKinds() []schema.GroupVersionKind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []schema.GroupVersionKind
	for _, state := range r.kartas {
		if state.err == nil {
			continue
		}
		kind := state.karta.Spec.StructureDefinition.RootComponent.Kind
		if kind != nil {
			result = append(result, schema.GroupVersionKind{Group: kind.Group, Version: kind.Version, Kind: kind.Kind})
		}
	}
	return result
}
