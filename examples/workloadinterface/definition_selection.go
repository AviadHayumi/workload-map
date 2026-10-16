// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

// SelectDefinitions demonstrates consumer policy for selected catalog entries
// and the complete current list from a synchronized external source. A watched
// definition overrides a catalog entry for the same exact root GVK. Removing
// that override restores the selected catalog entry. No other built-ins appear.
//
// Duplicate GVKs within either source are errors. The returned definitions are
// detached copies, sorted by exact GVK, with all metadata and status preserved.
// DefinitionRegistry.Replace subsequently validates and compiles the selection.
func SelectDefinitions(catalogSelection, watched []*v1alpha1.Karta) ([]*v1alpha1.Karta, error) {
	selected := make(map[schema.GroupVersionKind]*v1alpha1.Karta)
	for _, source := range []struct {
		name        string
		definitions []*v1alpha1.Karta
	}{{"catalog", catalogSelection}, {"watched", watched}} {
		seen := make(map[schema.GroupVersionKind]*v1alpha1.Karta)
		for i, definition := range source.definitions {
			if definition == nil {
				return nil, fmt.Errorf("%w: nil %s definition at index %d", discovery.ErrInvalidInput, source.name, i)
			}
			gvk := catalog.RootKey(definition)
			if gvk.Version == "" || gvk.Kind == "" {
				return nil, fmt.Errorf("%w: %s Karta %q requires root version and kind", discovery.ErrInvalidInput, source.name, definition.Name)
			}
			if previous, exists := seen[gvk]; exists {
				return nil, fmt.Errorf("%w: duplicate %s GVK %s: Karta %q (namespace %q, UID %q) and Karta %q (namespace %q, UID %q)",
					discovery.ErrInvalidInput, source.name, gvk,
					previous.Name, previous.Namespace, previous.UID, definition.Name, definition.Namespace, definition.UID)
			}
			seen[gvk] = definition
			selected[gvk] = definition
		}
	}
	keys := slices.SortedFunc(maps.Keys(selected), func(a, b schema.GroupVersionKind) int {
		return strings.Compare(a.String(), b.String())
	})
	definitions := make([]*v1alpha1.Karta, 0, len(keys))
	for _, gvk := range keys {
		definitions = append(definitions, selected[gvk].DeepCopy())
	}
	return definitions, nil
}
