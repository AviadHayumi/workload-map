// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

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
	// ErrNotReady reports a cluster source that has not completed its first sync.
	ErrNotReady = errors.New("definition sources not ready")
	// ErrUnavailable reports a cluster source that cannot be read, including a missing Karta CRD.
	ErrUnavailable = errors.New("definition source unavailable")
	// ErrPermissionDenied reports a cluster source the exporter is not allowed to read.
	ErrPermissionDenied = errors.New("definition source permission denied")

	errStopped = errors.New("inventory stopped")
)

// Inventory selects definitions from the catalog and the cluster's Karta objects;
// Run keeps the cluster side current. Reads never call the API server.
type Inventory struct {
	client        dynamic.Interface
	catalog       []*v1alpha1.Karta
	catalogFilter sourceFilter
	clusterFilter sourceFilter
	log           *slog.Logger

	current atomic.Pointer[published]

	mu                sync.Mutex
	started           bool
	store             cache.Store
	synced            bool
	failure           error
	stopped           bool
	unkeyable         []string
	unmatchedIncludes []string
}

type published struct {
	resolved snapshot
	err      error
}

var _ Interface = (*Inventory)(nil)

// New needs a client only when the cluster source is enabled.
func New(client dynamic.Interface, options Options) (*Inventory, error) {
	catalogFilter, clusterFilter, err := options.compile()
	if err != nil {
		return nil, err
	}
	if clusterFilter.enabled && client == nil {
		return nil, errors.New("the cluster definition source requires a client")
	}
	i := &Inventory{
		client:        client,
		catalog:       catalog.List(),
		catalogFilter: catalogFilter,
		clusterFilter: clusterFilter,
		log:           slog.Default(),
	}
	if !catalogFilter.enabled && !clusterFilter.enabled {
		i.log.Warn("both Karta definition sources are disabled; no workload has a definition")
	}
	if clusterFilter.enabled {
		i.current.Store(&published{err: ErrNotReady})
	} else {
		i.current.Store(&published{resolved: resolve(i.catalog, nil, catalogFilter, clusterFilter)})
	}
	return i, nil
}

// Run blocks until ctx ends. Afterwards cluster lookups fail; a catalog-only
// inventory keeps serving.
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

	// A successful call clears a failure; failures arrive through the watch
	// error handler, after the reflector gives up on an attempt, so a server
	// that only rejects streaming lists never reads as failed.
	resource := i.client.Resource(v1alpha1.GroupVersion.WithResource("kartas"))
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			list, err := resource.List(ctx, options)
			i.recover(err)
			return list, err
		},
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			w, err := resource.Watch(ctx, options)
			i.recover(err)
			return w, err
		},
	}
	informer := cache.NewSharedIndexInformerWithOptions(
		cache.ToListWatcherWithWatchListSemantics(lw, i.client),
		&unstructured.Unstructured{},
		cache.SharedIndexInformerOptions{ObjectDescription: "Karta definitions"})
	if err := informer.SetWatchErrorHandlerWithContext(func(ctx context.Context, r *cache.Reflector, err error) {
		i.update(func() { i.failure = err })
		cache.DefaultWatchErrorHandler(ctx, r, err)
	}); err != nil {
		return err
	}
	registration, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { i.update(nil) },
		UpdateFunc: func(any, any) { i.update(nil) },
		DeleteFunc: func(any) { i.update(nil) },
	})
	if err != nil {
		return err
	}
	i.update(func() { i.store = informer.GetStore() })

	var wg sync.WaitGroup
	wg.Go(func() { informer.RunWithContext(ctx) })
	if cache.WaitForCacheSync(ctx.Done(), registration.HasSynced) {
		i.update(func() { i.synced = true })
		i.log.Info("Karta definition inventory synced", "selected", len(i.current.Load().resolved.selections), "error", i.Ready())
	}
	wg.Wait()
	i.update(func() { i.stopped = true })
	return nil
}

func (i *Inventory) recover(err error) {
	if err != nil {
		return
	}
	i.update(func() { i.failure = nil })
}

// update applies change, then republishes from the informer store. The snapshot
// is rebuilt from the store on every change rather than patched per event.
func (i *Inventory) update(change func()) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if change != nil {
		change()
	}

	switch {
	case i.stopped:
		i.current.Store(&published{err: fmt.Errorf("%w: %w", ErrUnavailable, errStopped)})
		return
	case i.failure != nil && (apierrors.IsForbidden(i.failure) || apierrors.IsUnauthorized(i.failure)):
		i.current.Store(&published{err: fmt.Errorf("%w: %w", ErrPermissionDenied, i.failure)})
		return
	case i.failure != nil:
		i.current.Store(&published{err: fmt.Errorf("%w: %w", ErrUnavailable, i.failure)})
		return
	case !i.synced:
		i.current.Store(&published{err: ErrNotReady})
		return
	}

	objects := i.store.List()
	definitions := make([]*v1alpha1.Karta, 0, len(objects))
	for _, obj := range objects {
		u := obj.(*unstructured.Unstructured)
		var k v1alpha1.Karta
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &k); err != nil {
			i.current.Store(&published{err: fmt.Errorf("%w: decode Karta %q: %w", ErrUnavailable, u.GetName(), err)})
			return
		}
		definitions = append(definitions, &k)
	}
	resolved := resolve(i.catalog, definitions, i.catalogFilter, i.clusterFilter)

	// Log a report only when it changes; every event rebuilds the snapshot.
	if !slices.Equal(resolved.unkeyable, i.unkeyable) {
		i.unkeyable = resolved.unkeyable
		if len(i.unkeyable) > 0 {
			i.log.Warn("ignoring cluster Karta definitions whose root has no version or kind", "names", i.unkeyable)
		}
	}
	if !slices.Equal(resolved.unmatchedIncludes, i.unmatchedIncludes) {
		i.unmatchedIncludes = resolved.unmatchedIncludes
		if len(i.unmatchedIncludes) > 0 {
			i.log.Warn("cluster include names match no Karta definition", "names", i.unmatchedIncludes)
		}
	}
	i.current.Store(&published{resolved: resolved})
}

// Ready is nil once lookups can succeed.
func (i *Inventory) Ready() error {
	return i.current.Load().err
}

// Lookup ignores the requested version and never falls back while a source fails.
func (i *Inventory) Lookup(gvk schema.GroupVersionKind) (Selection, error) {
	p := i.current.Load()
	if p.err != nil {
		return Selection{}, p.err
	}
	return p.resolved.lookup(gvk)
}

// Selections returns nothing while a source fails.
func (i *Inventory) Selections() ([]Selection, error) {
	p := i.current.Load()
	if p.err != nil {
		return nil, p.err
	}
	return p.resolved.list()
}
