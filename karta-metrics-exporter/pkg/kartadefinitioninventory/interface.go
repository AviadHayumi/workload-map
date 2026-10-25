// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"errors"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

// ErrInvalidDefinition reports a selected definition that fails the SDK validator.
var ErrInvalidDefinition = errors.New("invalid Karta definition")

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
