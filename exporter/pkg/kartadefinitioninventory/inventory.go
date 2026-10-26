// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package kartadefinitioninventory selects caller-owned Karta snapshots.
package kartadefinitioninventory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

type Interface interface {
	Lookup(schema.GroupVersionKind) (Selection, error)
}

type Selection struct {
	Definition *v1alpha1.Karta
	GVK        schema.GroupVersionKind
	Origin     Origin
}

type Origin struct {
	Source          Source
	Name            string
	UID             types.UID
	ResourceVersion string
}

type Source string

const (
	SourceCatalog Source = "catalog"
	SourceCluster Source = "cluster"
)

var (
	ErrNotReady          = errors.New("definition sources not ready")
	ErrPending           = ErrNotReady
	ErrUnavailable       = errors.New("definition source unavailable")
	ErrPermissionDenied  = errors.New("definition source permission denied")
	ErrInvalidDefinition = errors.New("invalid definition")
)

var _ Interface = (*Inventory)(nil)

// SourceOptions filters definitions before source precedence is applied.
type SourceOptions struct {
	Enabled       bool
	Include       []string
	Exclude       []string
	LabelSelector *metav1.LabelSelector
}

type Options struct{ Catalog, Cluster SourceOptions }

func DefaultOptions() Options {
	return Options{Catalog: SourceOptions{Exclude: []string{"core-pod-v1"}}, Cluster: SourceOptions{Enabled: true}}
}

type filter struct {
	enabled          bool
	include, exclude map[string]bool
	selector         labels.Selector
}

type candidate struct {
	selection Selection
	validate  func() error
}

// Inventory owns the cluster LIST/WATCH lifecycle. Run may be called once.
// With cluster disabled, catalog lookups are ready immediately after New.
type Inventory struct {
	mu            sync.RWMutex
	client        dynamic.Interface
	clusterFilter filter
	catalog       map[schema.GroupKind]candidate
	cluster       map[string]*v1alpha1.Karta
	selected      map[schema.GroupKind]candidate
	sourceErr     error
	started       bool
}

func New(client dynamic.Interface, options Options) (*Inventory, error) {
	if options.Cluster.Enabled && client == nil {
		return nil, errors.New("cluster definitions require a dynamic client")
	}
	i, err := newInventory(options, catalog.List(), nil)
	if err != nil {
		return nil, err
	}
	i.client = client
	if options.Cluster.Enabled {
		i.sourceErr = ErrNotReady
	}
	return i, nil
}

// Snapshot is an immutable selection built from complete accepted cluster facts.
// Its caller establishes source readiness before construction.
type Snapshot struct{ inventory *Inventory }

var _ Interface = (*Snapshot)(nil)

func NewSnapshot(options Options, clusterDefs []*v1alpha1.Karta) (*Snapshot, error) {
	i, err := newInventory(options, catalog.List(), clusterDefs)
	if err != nil {
		return nil, err
	}
	return &Snapshot{inventory: i}, nil
}

// Candidate returns the elected definition without SDK validation.
func (s *Snapshot) Candidate(gvk schema.GroupVersionKind) (Selection, bool) {
	c, ok := s.inventory.selected[gvk.GroupKind()]
	if !ok {
		return Selection{}, false
	}
	selected := c.selection
	selected.Definition = selected.Definition.DeepCopy()
	return selected, true
}

func (s *Snapshot) Lookup(gvk schema.GroupVersionKind) (Selection, error) {
	return s.inventory.Lookup(gvk)
}

func (s *Snapshot) Selections() ([]Selection, error) {
	return s.inventory.Selections()
}

func newInventory(options Options, defs, clusterDefs []*v1alpha1.Karta) (*Inventory, error) {
	cf, err := newFilter(options.Catalog)
	if err != nil {
		return nil, fmt.Errorf("catalog filter: %w", err)
	}
	kf, err := newFilter(options.Cluster)
	if err != nil {
		return nil, fmt.Errorf("cluster filter: %w", err)
	}

	names := map[string]bool{}
	for _, d := range defs {
		if d == nil {
			return nil, errors.New("nil catalog definition")
		}
		names[d.Name] = true
	}
	for _, name := range append(append([]string{}, options.Catalog.Include...), options.Catalog.Exclude...) {
		if !names[name] {
			return nil, fmt.Errorf("unknown catalog definition %q", name)
		}
	}
	i := &Inventory{clusterFilter: kf, catalog: map[schema.GroupKind]candidate{}, cluster: map[string]*v1alpha1.Karta{}}
	for _, d := range defs {
		if !cf.matches(d) {
			continue
		}
		gk := catalog.RootKey(d).GroupKind()
		old, ok := i.catalog[gk]
		if !ok || d.Name < old.selection.Definition.Name {
			i.catalog[gk] = makeCandidate(d, SourceCatalog)
		}
	}
	for _, d := range clusterDefs {
		if d == nil {
			return nil, errors.New("nil cluster definition")
		}
		i.cluster[d.Name] = d.DeepCopy()
	}
	i.rebuild()
	return i, nil
}

func newFilter(o SourceOptions) (filter, error) {
	f := filter{enabled: o.Enabled, include: map[string]bool{}, exclude: map[string]bool{}}
	for _, name := range o.Include {
		f.include[name] = true
	}
	for _, name := range o.Exclude {
		f.exclude[name] = true
	}
	if o.LabelSelector != nil && (len(o.LabelSelector.MatchLabels) > 0 || len(o.LabelSelector.MatchExpressions) > 0) {
		var err error
		f.selector, err = metav1.LabelSelectorAsSelector(o.LabelSelector.DeepCopy())
		if err != nil {
			return f, err
		}
	}
	return f, nil
}

func (f filter) matches(d *v1alpha1.Karta) bool {
	return f.enabled && !f.exclude[d.Name] && ((len(f.include) == 0 && f.selector == nil) || f.include[d.Name] || (f.selector != nil && f.selector.Matches(labels.Set(d.Labels))))
}

func makeCandidate(d *v1alpha1.Karta, source Source) candidate {
	return makeCandidateWithValidator(d, source, func(definition *v1alpha1.Karta) error {
		return v1alpha1.NewKartaValidator(definition).Validate()
	})
}

func makeCandidateWithValidator(d *v1alpha1.Karta, source Source, validate func(*v1alpha1.Karta) error) candidate {
	owned := d.DeepCopy()
	origin := Origin{Source: source, Name: owned.Name}
	if source == SourceCluster {
		origin.UID = owned.UID
		origin.ResourceVersion = owned.ResourceVersion
	}
	return candidate{
		selection: Selection{Definition: owned, GVK: catalog.RootKey(owned), Origin: origin},
		validate: sync.OnceValue(func() error {
			validation := owned.DeepCopy()
			// Status interpretation is optional for the exporter, unlike SDK validation.
			if validation.Spec.StructureDefinition.RootComponent.StatusDefinition == nil {
				validation.Spec.StructureDefinition.RootComponent.StatusDefinition = &v1alpha1.StatusDefinition{}
			}
			return validate(validation)
		}),
	}
}

// rebuild requires the write lock once Run has started.
func (i *Inventory) rebuild() {
	selected := make(map[schema.GroupKind]candidate, len(i.catalog))
	for key, c := range i.catalog {
		selected[key] = c
	}
	winners := map[schema.GroupKind]*v1alpha1.Karta{}
	for _, d := range i.cluster {
		if !i.clusterFilter.matches(d) {
			continue
		}
		key := catalog.RootKey(d).GroupKind()
		old := winners[key]
		if old == nil || d.CreationTimestamp.Before(&old.CreationTimestamp) || (d.CreationTimestamp.Equal(&old.CreationTimestamp) && d.Name < old.Name) {
			winners[key] = d
		}
	}
	for key, d := range winners {
		selected[key] = makeCandidate(d, SourceCluster)
	}
	i.selected = selected
}

func (i *Inventory) Ready() error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.sourceErr
}

func (i *Inventory) Lookup(gvk schema.GroupVersionKind) (Selection, error) {
	i.mu.RLock()
	sourceErr := i.sourceErr
	c, ok := i.selected[gvk.GroupKind()]
	i.mu.RUnlock()
	if sourceErr != nil {
		return Selection{}, sourceErr
	}
	if !ok {
		return Selection{}, nil
	}
	if err := c.validate(); err != nil {
		return Selection{}, fmt.Errorf("%w %q: %w", ErrInvalidDefinition, c.selection.Origin.Name, err)
	}
	s := c.selection
	s.Definition = s.Definition.DeepCopy()
	return s, nil
}

func (i *Inventory) Run(ctx context.Context) error {
	i.mu.Lock()
	if i.started {
		i.mu.Unlock()
		return errors.New("definition inventory already started")
	}
	i.started = true
	i.mu.Unlock()
	if !i.clusterFilter.enabled {
		<-ctx.Done()
		return nil
	}
	defer i.fail(errors.New("inventory stopped"))
	for ctx.Err() == nil {
		err := i.synchronize(ctx)
		if ctx.Err() != nil {
			break
		}
		if err == nil {
			continue
		}
		i.fail(fmt.Errorf("cluster definitions: %w", err))
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}

func (i *Inventory) fail(err error) {
	category := ErrUnavailable
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		category = ErrPermissionDenied
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.sourceErr = fmt.Errorf("%w: %w", category, err)
}

// synchronize publishes only after every LIST page and WATCH initialization
// succeeds. A normal close relists without revoking the current selection.
// Failed or very short empty watches invalidate the source until recovery.
func (i *Inventory) synchronize(ctx context.Context) error {
	resource := i.client.Resource(v1alpha1.GroupVersion.WithResource("kartas"))
	defs := map[string]*v1alpha1.Karta{}
	options := metav1.ListOptions{Limit: 500}
	var rv string
	seen := map[string]bool{}
	for {
		pageContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		list, err := resource.List(pageContext, options)
		cancel()
		if err != nil {
			return err
		}
		if rv != "" && rv != list.GetResourceVersion() {
			return errors.New("definition LIST resource version changed between pages")
		}
		rv = list.GetResourceVersion()
		for j := range list.Items {
			d, err := decode(&list.Items[j])
			if err != nil {
				return err
			}
			defs[d.Name] = d
		}
		options.Continue = list.GetContinue()
		if options.Continue != "" && seen[options.Continue] {
			return errors.New("repeated definition LIST continuation token")
		}
		seen[options.Continue] = true
		if options.Continue == "" {
			break
		}
	}
	stream, err := resource.Watch(ctx, metav1.ListOptions{ResourceVersion: rv, AllowWatchBookmarks: true})
	if err != nil {
		return err
	}
	defer stream.Stop()
	started := time.Now()
	received := false
	i.mu.Lock()
	i.cluster = defs
	i.rebuild()
	i.sourceErr = nil
	i.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-stream.ResultChan():
			if !ok {
				if received || time.Since(started) >= time.Second {
					return nil
				}
				return errors.New("definition watch closed before observations")
			}
			received = true
			if event.Type == watch.Error {
				return apierrors.FromObject(event.Object)
			}
			if event.Type == watch.Bookmark {
				continue
			}
			obj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				return fmt.Errorf("unexpected definition watch object %T", event.Object)
			}
			d, err := decode(obj)
			if err != nil {
				return err
			}
			i.mu.Lock()
			switch event.Type {
			case watch.Added, watch.Modified:
				i.cluster[d.Name] = d
			case watch.Deleted:
				if old := i.cluster[d.Name]; old != nil && old.UID == d.UID {
					delete(i.cluster, d.Name)
				}
			default:
				i.mu.Unlock()
				return fmt.Errorf("unexpected definition watch event %q", event.Type)
			}
			i.rebuild()
			i.mu.Unlock()
		}
	}
}

func decode(obj *unstructured.Unstructured) (*v1alpha1.Karta, error) {
	d := new(v1alpha1.Karta)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, d); err != nil {
		return nil, fmt.Errorf("decode Karta %q: %w", obj.GetName(), err)
	}
	if d.Name == "" {
		return nil, errors.New("karta has no name")
	}
	return d, nil
}

// Selections returns sorted, caller-owned snapshots for initial watch setup.
// Invalid winners retain their definition, GVK and origin and are also reported
// as a joined ErrInvalidDefinition error. Lookup remains strict.
// Unavailable sources return no selections and a source error.
func (i *Inventory) Selections() ([]Selection, error) {
	i.mu.RLock()
	sourceErr := i.sourceErr
	candidates := i.selected
	i.mu.RUnlock()
	if sourceErr != nil {
		return nil, sourceErr
	}
	selections := make([]Selection, 0, len(candidates))
	var errs []error
	for _, c := range candidates {
		if err := c.validate(); err != nil {
			errs = append(errs, fmt.Errorf("%w %q: %w", ErrInvalidDefinition, c.selection.Origin.Name, err))
		}
		s := c.selection
		s.Definition = s.Definition.DeepCopy()
		selections = append(selections, s)
	}
	sort.Slice(selections, func(a, b int) bool { return selections[a].GVK.String() < selections[b].GVK.String() })
	return selections, errors.Join(errs...)
}
