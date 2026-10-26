// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

// ErrInvalidOptions is wrapped by every option problem; all problems are joined.
var ErrInvalidOptions = errors.New("invalid definition source options")

// Options selects definitions from the built-in catalog and from the cluster.
type Options struct {
	Catalog SourceOptions
	Cluster SourceOptions
}

// SourceOptions selects a definition when its name is in Include and its labels
// match LabelSelector; an empty list or selector allows all, and Exclude wins.
type SourceOptions struct {
	Enabled bool
	Include []string
	Exclude []string
	// LabelSelector applies to cluster definitions only.
	LabelSelector *metav1.LabelSelector
}

// DefaultOptions reads the cluster's Karta definitions and leaves the catalog off.
func DefaultOptions() Options {
	return Options{Cluster: SourceOptions{Enabled: true}}
}

// compile checks catalog names even when the catalog is disabled.
func (o Options) compile() (sourceFilter, sourceFilter, error) {
	known := sets.New[string]()
	for _, k := range catalog.List() {
		known.Insert(k.Name)
	}
	catalogFilter, catalogErrs := compileSource(SourceCatalog, o.Catalog, known)
	clusterFilter, clusterErrs := compileSource(SourceCluster, o.Cluster, nil)
	if err := errors.Join(append(catalogErrs, clusterErrs...)...); err != nil {
		return sourceFilter{}, sourceFilter{}, err
	}
	return catalogFilter, clusterFilter, nil
}

// compileSource skips the name check when known is nil.
func compileSource(source Source, o SourceOptions, known sets.Set[string]) (sourceFilter, []error) {
	var errs []error
	compileNames := func(field string, names []string) sets.Set[string] {
		set := sets.New[string]()
		for _, name := range names {
			var problem string
			switch {
			case name == "":
				problem = "empty definition name"
			case set.Has(name):
				problem = fmt.Sprintf("duplicate definition name %q", name)
			case known != nil && !known.Has(name):
				problem = fmt.Sprintf("unknown definition %q; known: %s", name, strings.Join(sets.List(known), ", "))
			}
			if problem != "" {
				errs = append(errs, fmt.Errorf("%w: %s.%s: %s", ErrInvalidOptions, source, field, problem))
			}
			set.Insert(name)
		}
		return set
	}

	f := sourceFilter{enabled: o.Enabled, include: compileNames("include", o.Include), exclude: compileNames("exclude", o.Exclude)}

	// LabelSelectorAsSelector(nil) matches nothing; here nil means no selector.
	switch selector := o.LabelSelector; {
	case selector == nil:
	case source == SourceCatalog && len(selector.MatchLabels)+len(selector.MatchExpressions) > 0:
		errs = append(errs, fmt.Errorf("%w: %s.labelSelector: catalog definitions carry no labels; select them by name", ErrInvalidOptions, source))
	default:
		compiled, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: %s.labelSelector: %w", ErrInvalidOptions, source, err))
			break
		}
		f.selector = compiled
	}
	return f, errs
}
