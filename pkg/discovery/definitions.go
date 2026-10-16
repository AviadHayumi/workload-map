// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
)

// DefinitionProvider supplies immutable compiled definitions by exact root GVK.
// A nil definition with no error means confirmed absence. Readiness or lookup
// failures must return an error; they must not be reported as absent definitions.
// Implementations support concurrent calls and own their readiness and lifecycle.
// A Resolver calls Definition once per workload observation and retains that
// result even if a newer definition is published during the query.
type DefinitionProvider interface {
	Definition(context.Context, schema.GroupVersionKind) (*Definition, error)
}

// Definition owns one validated immutable Karta and its compiled attribution
// plan. Construct it with CompileDefinition; the zero value is not usable.
// Reuse this handle until the definition changes, including across observations.
type Definition struct {
	gvk       schema.GroupVersionKind
	plan      *attribution.Plan
	cacheable bool
}

// DefinitionRegistry atomically publishes a complete selected set of Kartas.
// It has no implicit catalog fallback and starts no watches or goroutines.
// Its zero value is an intentionally empty, ready provider.
//
// Callers serialize reading their authoritative source and calling Replace so
// an older list cannot replace newer state. After informer synchronization, an
// external add, update or delete callback can republish the current selected
// list. The caller owns retries and requeuing workloads after definition changes.
type DefinitionRegistry struct {
	writeMu sync.Mutex
	mu      sync.RWMutex
	entries map[schema.GroupVersionKind]*Definition
}

var _ DefinitionProvider = (*DefinitionRegistry)(nil)

// CompileDefinition validates and copies a Karta without reading Kubernetes or
// evaluating workload expressions. It is intended for publication, not queries.
func CompileDefinition(karta *v1alpha1.Karta) (*Definition, error) {
	if karta == nil {
		return nil, fmt.Errorf("%w: nil definition", ErrInvalidInput)
	}
	gvk := catalog.RootKey(karta)
	if gvk.Version == "" || gvk.Kind == "" {
		return nil, fmt.Errorf("%w: definition %q requires root GVK", ErrInvalidInput, definitionSource(karta))
	}
	plan, err := attribution.Compile(karta)
	if err != nil {
		return nil, fmt.Errorf("definition %q for %s: %w", definitionSource(karta), gvk, err)
	}
	return &Definition{gvk: gvk, plan: plan, cacheable: cacheableAttribution(plan.Definition())}, nil
}

// GVK returns the exact root group, version and kind for this definition.
func (d *Definition) GVK() schema.GroupVersionKind { return d.gvk }

// Karta returns a caller-owned copy for offline component or tree inspection.
func (d *Definition) Karta() *v1alpha1.Karta { return d.plan.Definition() }

// NewDefinitionRegistry validates and publishes only the supplied definitions.
// Supplying no definitions creates an intentionally empty, ready provider.
func NewDefinitionRegistry(definitions ...*v1alpha1.Karta) (*DefinitionRegistry, error) {
	r := &DefinitionRegistry{}
	if err := r.Replace(definitions...); err != nil {
		return nil, err
	}
	return r, nil
}

// Replace validates the entire selection before atomically publishing it.
// Missing entries are removed. Any invalid or duplicate exact GVK rejects the
// whole replacement and leaves the previous selection available. A consumer
// that must stop serving after a rejected update can gate provider reads with
// its own error state. Unchanged definitions reuse their compiled handles,
// preserving attribution cache hits. Equality covers the whole Karta, including
// TypeMeta, metadata and status, because Snapshot compares complete definitions.
// Metadata-only changes therefore publish a new handle and miss attribution
// cache entries. Resource versions are opaque; Replace does not infer ordering.
func (r *DefinitionRegistry) Replace(definitions ...*v1alpha1.Karta) error {
	if r == nil {
		return fmt.Errorf("%w: nil definition registry", ErrInvalidInput)
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.mu.RLock()
	previous := r.entries
	r.mu.RUnlock()
	entries := make(map[schema.GroupVersionKind]*Definition, len(definitions))
	for _, karta := range definitions {
		if karta == nil {
			return fmt.Errorf("%w: nil definition", ErrInvalidInput)
		}
		gvk := catalog.RootKey(karta)
		if first, exists := entries[gvk]; exists {
			return fmt.Errorf("%w: duplicate definitions for %s: %q and %q", ErrInvalidInput, gvk, definitionSource(first.Karta()), definitionSource(karta))
		}
		if old := previous[gvk]; old != nil && reflect.DeepEqual(karta, old.Karta()) {
			entries[gvk] = old
			continue
		}
		definition, err := CompileDefinition(karta)
		if err != nil {
			return err
		}
		entries[gvk] = definition
	}
	r.mu.Lock()
	r.entries = entries
	r.mu.Unlock()
	return nil
}

// Definition returns an immutable handle or nil when this exact GVK is absent.
// An empty ready registry confirms absence; a not-yet-synchronized external
// source must gate access with its own DefinitionProvider implementation.
func (r *DefinitionRegistry) Definition(ctx context.Context, gvk schema.GroupVersionKind) (*Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("%w: nil definition registry", ErrInvalidInput)
	}
	r.mu.RLock()
	definition := r.entries[gvk]
	r.mu.RUnlock()
	return definition, nil
}

func definitionSource(karta *v1alpha1.Karta) string {
	name := karta.Name
	if name == "" {
		name = "<unnamed>"
	}
	if karta.Namespace != "" {
		return karta.Namespace + "/" + name
	}
	return name
}
