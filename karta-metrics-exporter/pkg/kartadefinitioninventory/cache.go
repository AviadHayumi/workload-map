// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

// ClusterSource supplies the cluster-scoped Karta API's LIST and WATCH operations.
type ClusterSource interface {
	List(context.Context, metav1.ListOptions) (*v1alpha1.KartaList, error)
	Watch(context.Context, metav1.ListOptions) (watch.Interface, error)
}

type Inventory struct {
	mu       sync.RWMutex
	config   Config
	selector labels.Selector
	cluster  map[string]*v1alpha1.Karta
	catalog  map[schema.GroupKind]cachedSelection
	selected map[schema.GroupKind]cachedSelection
	source   ClusterSource
	err      error
	running  bool
}

type cachedSelection struct {
	selection Selection
	err       error
}

var ErrInvalidDefinition = errors.New("invalid Karta definition")

var ErrPending = errors.New("karta inventory not synchronized")
var ErrUnavailable = errors.New("karta inventory unavailable")

func New(config Config, source ClusterSource) (*Inventory, error) {
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
	i := &Inventory{config: config, selector: selector, source: source, cluster: make(map[string]*v1alpha1.Karta)}
	if config.Cluster.Enabled {
		i.err = ErrPending
		if source == nil {
			return nil, errors.New("cluster source required")
		}
	}
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
	if i.err != nil {
		return Selection{}, i.err
	}
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

// Run maintains a complete snapshot. A failed stream hides the old snapshot until
// a fresh LIST and WATCH are established, so failure cannot enable fallback.
func (i *Inventory) Run(ctx context.Context) error {
	if !i.config.Cluster.Enabled {
		return nil
	}
	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return errors.New("inventory already running")
	}
	i.running = true
	i.mu.Unlock()
	defer func() { i.mu.Lock(); i.running = false; i.err = ErrPending; i.mu.Unlock() }()
	for ctx.Err() == nil {
		err := i.stream(ctx)
		i.mu.Lock()
		i.err = fmt.Errorf("%w: %w", ErrUnavailable, err)
		i.mu.Unlock()
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (i *Inventory) stream(ctx context.Context) error {
	options := metav1.ListOptions{LabelSelector: i.selector.String()}
	list, err := i.source.List(ctx, options)
	if err != nil {
		return err
	}
	options.ResourceVersion = list.ResourceVersion
	stream, err := i.source.Watch(ctx, options)
	if err != nil {
		return err
	}
	defer stream.Stop()
	snapshot := make(map[string]*v1alpha1.Karta)
	for idx := range list.Items {
		k := &list.Items[idx]
		if selected(k.Name, i.config.Cluster.Include, i.config.Cluster.Exclude) && i.selector.Matches(labels.Set(k.Labels)) {
			snapshot[k.Name] = k.DeepCopy()
		}
	}
	i.mu.Lock()
	i.cluster = snapshot
	i.selected = elect(snapshot, SourceCluster)
	i.err = nil
	i.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-stream.ResultChan():
			if !ok {
				return errors.New("karta watch closed")
			}
			if event.Type == watch.Bookmark {
				continue
			}
			if event.Type == watch.Error {
				return apierrors.FromObject(event.Object)
			}
			k, ok := event.Object.(*v1alpha1.Karta)
			if !ok {
				return fmt.Errorf("unexpected Karta watch object %T", event.Object)
			}
			i.mu.Lock()
			switch event.Type {
			case watch.Deleted:
				if old := i.cluster[k.Name]; old != nil && old.UID == k.UID {
					delete(i.cluster, k.Name)
				}
			case watch.Added, watch.Modified:
				if selected(k.Name, i.config.Cluster.Include, i.config.Cluster.Exclude) && i.selector.Matches(labels.Set(k.Labels)) {
					i.cluster[k.Name] = k.DeepCopy()
				} else {
					delete(i.cluster, k.Name)
				}
			}
			i.selected = elect(i.cluster, SourceCluster)
			i.mu.Unlock()
		}
	}
}

// Selections returns the selected definition for each known group and kind.
func (i *Inventory) Selections() ([]Selection, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.err != nil {
		return nil, i.err
	}
	keys := make(map[schema.GroupKind]schema.GroupVersionKind)
	for key := range i.selected {
		keys[key] = schema.GroupVersionKind{Group: key.Group, Kind: key.Kind}
	}
	for key := range i.catalog {
		keys[key] = schema.GroupVersionKind{Group: key.Group, Kind: key.Kind}
	}
	result := make([]Selection, 0, len(keys))
	for _, gvk := range keys {
		selection, err := i.lookup(gvk)
		if err != nil && !errors.Is(err, ErrInvalidDefinition) {
			return nil, err
		}
		if selection.Definition != nil {
			result = append(result, selection)
		}
	}
	slices.SortFunc(result, func(a, b Selection) int { return strings.Compare(a.GVK.String(), b.GVK.String()) })
	return result, nil
}

// ClusterOrigins reports the filtered cluster snapshot for comparison with an
// independent observer. Resource versions are opaque equality tokens.
func (i *Inventory) ClusterOrigins() ([]Origin, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.err != nil {
		return nil, i.err
	}
	origins := make([]Origin, 0, len(i.cluster))
	for _, k := range i.cluster {
		origins = append(origins, Origin{Source: SourceCluster, Name: k.Name, UID: k.UID, ResourceVersion: k.ResourceVersion})
	}
	slices.SortFunc(origins, func(a, b Origin) int { return strings.Compare(a.Name, b.Name) })
	return origins, nil
}
