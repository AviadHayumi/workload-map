// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

// Snapshot is an immutable selection over given cluster definitions, without source state.
type Snapshot struct {
	resolved snapshot
}

var _ Interface = (*Snapshot)(nil)

// NewSnapshot copies clusterDefinitions.
func NewSnapshot(options Options, clusterDefinitions []*v1alpha1.Karta) (*Snapshot, error) {
	catalogFilter, clusterFilter, err := options.compile()
	if err != nil {
		return nil, err
	}
	owned := make([]*v1alpha1.Karta, len(clusterDefinitions))
	for i, k := range clusterDefinitions {
		if k == nil {
			return nil, fmt.Errorf("cluster definition %d is nil", i)
		}
		owned[i] = k.DeepCopy()
	}
	return &Snapshot{resolved: resolve(catalog.List(), owned, catalogFilter, clusterFilter)}, nil
}

// Lookup ignores the requested version; a zero Selection with no error means no definition.
func (s *Snapshot) Lookup(gvk schema.GroupVersionKind) (Selection, error) {
	return s.resolved.lookup(gvk)
}

// Candidate returns the elected definition without validating it.
func (s *Snapshot) Candidate(gvk schema.GroupVersionKind) (Selection, bool) {
	return s.resolved.elected(gvk)
}

// Selections includes invalid winners and reports each in the joined error.
func (s *Snapshot) Selections() ([]Selection, error) {
	return s.resolved.list()
}
