// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

type Inventory struct {
	mu       sync.RWMutex
	config   Config
	selector labels.Selector
	catalog  map[schema.GroupKind]cachedSelection
	selected map[schema.GroupKind]cachedSelection
}

type cachedSelection struct {
	selection Selection
	err       error
}

// Interface looks up the selected Karta definition for a workload type.
type Interface interface {
	Lookup(schema.GroupVersionKind) (Selection, error)
}

// Selection pairs a definition with the source it came from.
type Selection struct {
	Definition *v1alpha1.Karta
	GVK        schema.GroupVersionKind
	Origin     Origin
}

// Origin identifies the definition. Catalog entries have no UID or resource version.
type Origin struct {
	Source          Source
	Name            string
	UID             types.UID
	ResourceVersion string
}

type Source string

// Config selects definitions before they enter the lookup caches.
type Config struct {
	Catalog CatalogConfig
	Cluster ClusterConfig
}

type CatalogConfig struct {
	Enabled bool
	Include []string
	Exclude []string
}

type ClusterConfig struct {
	Enabled       bool
	Include       []string
	Exclude       []string
	LabelSelector *metav1.LabelSelector
}

const (
	SourceCatalog Source = "catalog"
	SourceCluster Source = "cluster"
)

var ErrInvalidDefinition = errors.New("invalid Karta definition")

func New(config Config) (*Inventory, error) {
	selector, err := metav1.LabelSelectorAsSelector(config.Cluster.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("cluster selector: %w", err)
	}
	if config.Cluster.LabelSelector == nil {
		selector = labels.Everything()
	}
	config.Cluster.Include = slices.Clone(config.Cluster.Include)
	config.Cluster.Exclude = slices.Clone(config.Cluster.Exclude)
	config.Catalog.Include = slices.Clone(config.Catalog.Include)
	config.Catalog.Exclude = slices.Clone(config.Catalog.Exclude)
	i := &Inventory{config: config, selector: selector}
	known := make(map[string]bool)
	for _, k := range catalog.List() {
		known[k.Name] = true
	}
	for _, name := range append(slices.Clone(config.Catalog.Include), config.Catalog.Exclude...) {
		if !known[name] {
			return nil, fmt.Errorf("unknown catalog definition %q", name)
		}
	}
	definitions := make(map[string]*v1alpha1.Karta)
	if config.Catalog.Enabled {
		for _, k := range catalog.List() {
			if selected(k.Name, config.Catalog.Include, config.Catalog.Exclude) {
				definitions[k.Name] = k
			}
		}
	}
	i.catalog = elect(definitions, SourceCatalog)
	i.selected = make(map[schema.GroupKind]cachedSelection)
	return i, nil
}

func selected(name string, include, exclude []string) bool {
	return !slices.Contains(exclude, name) && (len(include) == 0 || slices.Contains(include, name))
}

func (i *Inventory) Lookup(gvk schema.GroupVersionKind) (Selection, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.lookup(gvk)
}

func (i *Inventory) lookup(gvk schema.GroupVersionKind) (Selection, error) {
	entry, ok := i.selected[gvk.GroupKind()]
	if !ok {
		entry = i.catalog[gvk.GroupKind()]
	}
	result := entry.selection
	if result.Definition != nil {
		result.Definition = result.Definition.DeepCopy()
	}
	return result, entry.err
}

func elect(definitions map[string]*v1alpha1.Karta, source Source) map[schema.GroupKind]cachedSelection {
	winners := make(map[schema.GroupKind]*v1alpha1.Karta)
	for _, k := range definitions {
		key := catalog.RootKey(k).GroupKind()
		chosen := winners[key]
		if chosen == nil || k.CreationTimestamp.Before(&chosen.CreationTimestamp) || (k.CreationTimestamp.Equal(&chosen.CreationTimestamp) && k.Name < chosen.Name) {
			winners[key] = k
		}
	}
	result := make(map[schema.GroupKind]cachedSelection, len(winners))
	for key, k := range winners {
		origin := Origin{Source: source, Name: k.Name}
		if source == SourceCluster {
			origin.UID = k.UID
			origin.ResourceVersion = k.ResourceVersion
		}
		entry := cachedSelection{selection: Selection{Definition: k, GVK: catalog.RootKey(k), Origin: origin}}
		if err := v1alpha1.NewKartaValidator(k).Validate(); err != nil {
			entry.err = fmt.Errorf("%w: %s: %w", ErrInvalidDefinition, k.Name, err)
		}
		result[key] = entry
	}
	return result
}

// ReplaceCluster selects from the complete accepted cluster snapshot.
// The caller owns source synchronization, availability, and event ordering.
func (i *Inventory) ReplaceCluster(definitions []*v1alpha1.Karta) {
	selectedDefinitions := make(map[string]*v1alpha1.Karta, len(definitions))
	if i.config.Cluster.Enabled {
		for _, k := range definitions {
			if k != nil && selected(k.Name, i.config.Cluster.Include, i.config.Cluster.Exclude) && i.selector.Matches(labels.Set(k.Labels)) {
				selectedDefinitions[k.Name] = k.DeepCopy()
			}
		}
	}
	next := elect(selectedDefinitions, SourceCluster)
	i.mu.Lock()
	i.selected = next
	i.mu.Unlock()
}

// Selections returns the selected definition for each known group and kind.
func (i *Inventory) Selections() []Selection {
	i.mu.RLock()
	defer i.mu.RUnlock()
	keys := make(map[schema.GroupKind]schema.GroupVersionKind)
	for key := range i.selected {
		keys[key] = schema.GroupVersionKind{Group: key.Group, Kind: key.Kind}
	}
	for key := range i.catalog {
		keys[key] = schema.GroupVersionKind{Group: key.Group, Kind: key.Kind}
	}
	result := make([]Selection, 0, len(keys))
	for _, gvk := range keys {
		selection, _ := i.lookup(gvk)
		if selection.Definition != nil {
			result = append(result, selection)
		}
	}
	slices.SortFunc(result, func(a, b Selection) int { return strings.Compare(a.GVK.String(), b.GVK.String()) })
	return result
}
