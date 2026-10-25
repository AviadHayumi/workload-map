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
	catalog  []*v1alpha1.Karta
	source   ClusterSource
	err      error
	running  bool
}

var ErrPending = errors.New("Karta inventory not synchronized")
var ErrUnavailable = errors.New("Karta inventory unavailable")

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
	if config.Catalog.Enabled {
		for _, k := range catalog.List() {
			if selected(k.Name, config.Catalog.Include, config.Catalog.Exclude) {
				i.catalog = append(i.catalog, k)
			}
		}
	}
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
	var chosen *v1alpha1.Karta
	source := SourceCluster
	for _, k := range i.cluster {
		if catalog.RootKey(k).GroupKind() != gvk.GroupKind() {
			continue
		}
		if chosen == nil || k.CreationTimestamp.Before(&chosen.CreationTimestamp) || (k.CreationTimestamp.Equal(&chosen.CreationTimestamp) && k.Name < chosen.Name) {
			chosen = k
		}
	}
	if chosen == nil {
		source = SourceCatalog
		for _, k := range i.catalog {
			if catalog.RootKey(k).GroupKind() == gvk.GroupKind() && (chosen == nil || k.Name < chosen.Name) {
				chosen = k
			}
		}
	}
	if chosen == nil {
		return Selection{}, nil
	}
	origin := Origin{Source: source, Name: chosen.Name}
	if source == SourceCluster {
		origin.UID = chosen.UID
		origin.ResourceVersion = chosen.ResourceVersion
	}
	return Selection{Definition: chosen.DeepCopy(), GVK: catalog.RootKey(chosen), Origin: origin}, nil
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
	i.err = nil
	i.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-stream.ResultChan():
			if !ok {
				return errors.New("Karta watch closed")
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
	for _, k := range i.cluster {
		gvk := catalog.RootKey(k)
		keys[gvk.GroupKind()] = gvk
	}
	for _, k := range i.catalog {
		gvk := catalog.RootKey(k)
		keys[gvk.GroupKind()] = gvk
	}
	result := make([]Selection, 0, len(keys))
	for _, gvk := range keys {
		selection, err := i.lookup(gvk)
		if err != nil {
			return nil, err
		}
		if selection.Definition != nil {
			result = append(result, selection)
		}
	}
	slices.SortFunc(result, func(a, b Selection) int { return strings.Compare(a.GVK.String(), b.GVK.String()) })
	return result, nil
}
