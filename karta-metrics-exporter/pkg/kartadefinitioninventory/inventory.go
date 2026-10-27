// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

var (
	ErrNotReady          = errors.New("definition sources not ready")
	ErrUnavailable       = errors.New("definition source unavailable")
	ErrPermissionDenied  = errors.New("definition source permission denied")
	ErrInvalidDefinition = errors.New("invalid Karta definition")

	errStopped = errors.New("inventory stopped")
	errRelist  = errors.New("watch not resumed after a failure; relisting to confirm fresh data")
)

// Inventory serves definitions from the catalog and the cluster. Reads never
// call the API server.
type Inventory struct {
	client        dynamic.Interface
	catalog       map[schema.GroupKind]candidate
	clusterFilter sourceFilter
	log           *slog.Logger
	loaded        chan struct{}
	done          chan struct{}

	mu       sync.RWMutex
	cluster  map[string]clusterEntry
	started  bool
	synced   bool
	stopped  bool
	failure  error
	failures uint64
	// listedFrom is the failure count when the latest list began.
	listedFrom        uint64
	unkeyable         []string
	unmatchedIncludes []string
}

// Entries own their definitions and are replaced whole, so readers can retain
// a candidate after releasing the lock.
type clusterEntry struct {
	candidate candidate
	err       error
}

type reflectorStore struct {
	inventory *Inventory
}

var (
	_ Interface            = (*Inventory)(nil)
	_ cache.ReflectorStore = reflectorStore{}
)

// New needs a client only when the cluster source is enabled.
func New(client dynamic.Interface, options Options) (*Inventory, error) {
	catalogFilter, clusterFilter, err := options.compile()
	if err != nil {
		return nil, err
	}
	if clusterFilter.enabled && client == nil {
		return nil, errors.New("the cluster definition source requires a client")
	}
	fromCatalog, _ := selectDefinitions(catalog.List(), catalogFilter, SourceCatalog)
	i := &Inventory{
		client:        client,
		catalog:       fromCatalog,
		clusterFilter: clusterFilter,
		log:           slog.Default(),
		loaded:        make(chan struct{}),
		done:          make(chan struct{}),
		cluster:       make(map[string]clusterEntry),
	}
	if !catalogFilter.enabled && !clusterFilter.enabled {
		i.log.Warn("both Karta definition sources are disabled; no workload has a definition")
	}
	if !clusterFilter.enabled {
		i.synced = true
		close(i.loaded)
	}
	return i, nil
}

// Run blocks until ctx ends. Afterwards cluster lookups fail; a catalog-only
// inventory keeps serving. Run may be called only once.
func (i *Inventory) Run(ctx context.Context) error {
	i.mu.Lock()
	if i.started {
		i.mu.Unlock()
		return errors.New("definition inventory already started")
	}
	i.started = true
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		i.stopped = true
		close(i.done)
	}()
	if !i.clusterFilter.enabled {
		<-ctx.Done()
		return nil
	}

	resource := i.client.Resource(v1alpha1.GroupVersion.WithResource("kartas"))
	reflector := cache.NewReflectorWithOptions(
		cache.ToListWatcherWithWatchListSemantics(i.listWatch(resource), i.client),
		&unstructured.Unstructured{}, reflectorStore{inventory: i},
		cache.ReflectorOptions{Name: "karta-definitions", TypeDescription: "Karta definitions"})
	reflector.RunWithContext(ctx)
	return nil
}

// WaitLoaded waits for the first complete cluster list to be applied. It does
// not start Run or report later source failures; use Ready for current health.
func (i *Inventory) WaitLoaded(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-i.loaded:
		return nil
	case <-i.done:
		i.mu.RLock()
		defer i.mu.RUnlock()
		if i.synced {
			return nil
		}
		return fmt.Errorf("%w: %w", ErrUnavailable, errStopped)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// listWatch records every failure; the reflector retries some without reporting them.
func (i *Inventory) listWatch(resource dynamic.ResourceInterface) *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			// Later pages continue the same list.
			if options.Continue == "" {
				i.mu.Lock()
				i.listedFrom = i.failures
				i.mu.Unlock()
			}
			list, err := resource.List(ctx, options)
			if err != nil {
				i.mu.Lock()
				i.fail(err)
				i.mu.Unlock()
			}
			return list, err
		},
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			streaming := options.SendInitialEvents != nil && *options.SendInitialEvents
			i.mu.Lock()
			failed := i.failure != nil
			if streaming {
				i.listedFrom = i.failures
			}
			i.mu.Unlock()
			// A resumed watch never marks the end of its replay; only a new list is fresh.
			if failed && !streaming {
				return nil, errRelist
			}
			w, err := resource.Watch(ctx, options)
			switch {
			// A server without streaming lists answers invalid; LIST follows.
			case err != nil && streaming && apierrors.IsInvalid(err):
				return nil, err
			case err != nil:
				i.mu.Lock()
				i.fail(err)
				i.mu.Unlock()
				return nil, err
			}
			events := make(chan watch.Event)
			proxy := watch.NewProxyWatcher(events)
			go func() {
				defer close(events)
				defer w.Stop()
				for {
					select {
					case event, ok := <-w.ResultChan():
						if !ok {
							i.mu.Lock()
							if !proxy.Stopping() && ctx.Err() == nil {
								// A decoder failure can close a watch without an Error event.
								// Keep an earlier cause, but invalidate any list already in flight.
								cause := i.failure
								if cause == nil {
									cause = io.ErrUnexpectedEOF
								}
								i.fail(cause)
							}
							i.mu.Unlock()
							return
						}
						// Recorded before the reflector sees it. A stopped watch is abandoned.
						if event.Type == watch.Error {
							err := apierrors.FromObject(event.Object)
							i.mu.Lock()
							if !proxy.Stopping() && ctx.Err() == nil {
								i.fail(err)
							}
							i.mu.Unlock()
						}
						select {
						case events <- event:
						case <-proxy.StopChan():
							return
						case <-ctx.Done():
							return
						}
					case <-proxy.StopChan():
						return
					case <-ctx.Done():
						return
					}
				}
			}()
			return proxy, nil
		},
	}
}

// fail records err; callers hold i.mu.
func (i *Inventory) fail(err error) {
	i.failure = err
	i.failures++
}

func (s reflectorStore) Add(obj any) error {
	name, entry, err := decodeKarta(obj)
	if err != nil {
		return err
	}
	i := s.inventory
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cluster[name] = entry
	i.report()
	return nil
}

func (s reflectorStore) Update(obj any) error { return s.Add(obj) }

func (s reflectorStore) Delete(obj any) error {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected Karta object %T", obj)
	}
	i := s.inventory
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.cluster, u.GetName())
	i.report()
	return nil
}

func (s reflectorStore) Replace(list []any, _ string) error {
	cluster := make(map[string]clusterEntry, len(list))
	for _, obj := range list {
		name, entry, err := decodeKarta(obj)
		if err != nil {
			return err
		}
		cluster[name] = entry
	}
	i := s.inventory
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cluster = cluster
	// A failure after this list began is newer than the list.
	if i.failures == i.listedFrom {
		i.failure = nil
	}
	i.report()
	if !i.synced {
		i.synced = true
		close(i.loaded)
		i.log.Info("Karta definition inventory synced", "listed", len(list), "error", i.ready())
	}
	return nil
}

func (s reflectorStore) Resync() error { return nil }

func decodeKarta(obj any) (string, clusterEntry, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return "", clusterEntry{}, fmt.Errorf("unexpected Karta object %T", obj)
	}
	var k v1alpha1.Karta
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.DeepCopy().Object, &k); err != nil {
		return u.GetName(), clusterEntry{err: fmt.Errorf("%w: decode Karta %q: %w", ErrUnavailable, u.GetName(), err)}, nil
	}
	return u.GetName(), clusterEntry{candidate: newCandidate(&k, SourceCluster, validateDefinition)}, nil
}

// report logs diagnostics only when they change; callers hold i.mu.
func (i *Inventory) report() {
	var unkeyable, unmatched []string
	for name, entry := range i.cluster {
		c := entry.candidate
		if entry.err == nil && i.clusterFilter.matches(c.selection.Definition) &&
			(c.selection.GVK.Version == "" || c.selection.GVK.Kind == "") {
			unkeyable = append(unkeyable, name)
		}
	}
	for name := range i.clusterFilter.include {
		if _, ok := i.cluster[name]; !ok {
			unmatched = append(unmatched, name)
		}
	}
	slices.Sort(unkeyable)
	slices.Sort(unmatched)
	if !slices.Equal(unkeyable, i.unkeyable) {
		i.unkeyable = unkeyable
		if len(unkeyable) > 0 {
			i.log.Warn("ignoring cluster Karta definitions whose root has no version or kind", "names", unkeyable)
		}
	}
	if !slices.Equal(unmatched, i.unmatchedIncludes) {
		i.unmatchedIncludes = unmatched
		if len(unmatched) > 0 {
			i.log.Warn("cluster include names match no Karta definition", "names", unmatched)
		}
	}
}

// Ready is nil once lookups can succeed.
func (i *Inventory) Ready() error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.ready()
}

// ready checks source state; callers hold i.mu.
func (i *Inventory) ready() error {
	if !i.clusterFilter.enabled {
		return nil
	}
	switch {
	case i.stopped:
		return fmt.Errorf("%w: %w", ErrUnavailable, errStopped)
	case i.failure != nil && (apierrors.IsForbidden(i.failure) || apierrors.IsUnauthorized(i.failure)):
		return fmt.Errorf("%w: %w", ErrPermissionDenied, i.failure)
	case i.failure != nil:
		return fmt.Errorf("%w: %w", ErrUnavailable, i.failure)
	case !i.synced:
		return ErrNotReady
	}
	for _, entry := range i.cluster {
		if entry.err != nil {
			return entry.err
		}
	}
	return nil
}

// Lookup ignores the requested version and never falls back while a source fails.
func (i *Inventory) Lookup(gvk schema.GroupVersionKind) (Selection, error) {
	i.mu.RLock()
	if err := i.ready(); err != nil {
		i.mu.RUnlock()
		return Selection{}, err
	}
	gk := gvk.GroupKind()
	winner := i.catalog[gk]
	for _, entry := range i.cluster {
		c := entry.candidate
		if c.selection.GVK.GroupKind() == gk && c.selection.GVK.Version != "" && c.selection.GVK.Kind != "" &&
			i.clusterFilter.matches(c.selection.Definition) && c.preferredTo(winner) {
			winner = c
		}
	}
	i.mu.RUnlock()
	return winner.lookup()
}

// Selections returns nothing while a source fails.
func (i *Inventory) Selections() ([]Selection, error) {
	i.mu.RLock()
	if err := i.ready(); err != nil {
		i.mu.RUnlock()
		return nil, err
	}
	selected := maps.Clone(i.catalog)
	for _, entry := range i.cluster {
		c := entry.candidate
		gk := c.selection.GVK.GroupKind()
		if c.selection.GVK.Version != "" && c.selection.GVK.Kind != "" &&
			i.clusterFilter.matches(c.selection.Definition) && c.preferredTo(selected[gk]) {
			selected[gk] = c
		}
	}
	i.mu.RUnlock()
	return listCandidates(selected)
}
