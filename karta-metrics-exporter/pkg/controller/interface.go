// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"sync"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type Runner interface {
	// Run owns watches and workers until cancellation. It returns after they stop.
	Run(context.Context) error
}

type Dependencies struct {
	Discovery       discovery.Discoverer
	Inventory       inventory.Interface
	Labels          labelresolver.Resolver
	Publications    store.Writer
	PublicationLock *sync.Mutex
}
