// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package scaffold

import (
	"context"
	"errors"
	"sync"

	"github.com/stretchr/testify/mock"
	"go.opentelemetry.io/otel/metric"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/app"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/scaffold/mocks"
	collector "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/adapters/otel"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/controller"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

var ErrNotImplemented = errors.New("exporter component is not implemented")

// New is the temporary composition root. Replace the factories here as components land.
func New() app.Components {
	definitions := newInventory(inventory.Config{})
	reader := newObjectReader()
	ownership := newDiscovery(discovery.Dependencies{Reader: reader, Inventory: definitions})
	labels := newLabelResolver(labelresolver.Config{}, labelresolver.Dependencies{Compile: newLabelEvaluator})
	publications := newStore()
	lock := &sync.Mutex{}
	runtime := newRuntime(controller.Dependencies{
		Discovery: ownership, Inventory: definitions, Labels: labels,
		Publications: publications, PublicationLock: lock,
	})
	return app.Components{Runtime: runtime, Store: publications, PublicationLock: lock, Register: registerCollector}
}

func newInventory(_ inventory.Config) inventory.Interface {
	m := &mocks.Inventory{}
	m.EXPECT().Lookup(mock.Anything).Return(inventory.Selection{}, ErrNotImplemented)
	return m
}

func newObjectReader() discovery.ObjectReader {
	m := &mocks.ObjectReader{}
	m.EXPECT().Cached(mock.Anything).Return(nil, false)
	m.EXPECT().GetMetadata(mock.Anything, mock.Anything).Return(nil, ErrNotImplemented)
	return m
}

func newDiscovery(_ discovery.Dependencies) discovery.Discoverer {
	m := &mocks.Discovery{}
	m.EXPECT().Get(mock.Anything, mock.Anything).Return(discovery.Result{}, ErrNotImplemented)
	return m
}

func newLabelEvaluator(_ labelevaluator.Config) (labelevaluator.Evaluator, error) {
	m := &mocks.LabelEvaluator{}
	m.EXPECT().Evaluate(mock.Anything, mock.Anything).Return("", ErrNotImplemented)
	return m, nil
}

func newLabelResolver(_ labelresolver.Config, _ labelresolver.Dependencies) labelresolver.Resolver {
	m := &mocks.LabelResolver{}
	m.EXPECT().Resolve(mock.Anything, mock.Anything).Return(nil, ErrNotImplemented)
	return m
}

func newStore() store.Interface {
	m := &mocks.Store{}
	m.EXPECT().BindPublication(mock.Anything, mock.Anything).Return().Once()
	m.EXPECT().Capture().Return(store.CapturedView{})
	// Writes are deliberately unconfigured: a real worker must use a real Store.
	return m
}

func newRuntime(_ controller.Dependencies) controller.Runner {
	m := &mocks.Runtime{}
	m.EXPECT().Run(mock.Anything).RunAndReturn(func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	return m
}

func registerCollector(meter metric.Meter, _ collector.Capturer, _ collector.Options) (metric.Registration, error) {
	// Register no instruments. The scaffold must not emit production-looking series.
	return meter.RegisterCallback(func(context.Context, metric.Observer) error { return nil })
}

// Keep generated mocks checked against their contracts even before implementations exist.
var (
	_ inventory.Interface      = (*mocks.Inventory)(nil)
	_ discovery.Discoverer     = (*mocks.Discovery)(nil)
	_ discovery.ObjectReader   = (*mocks.ObjectReader)(nil)
	_ labelevaluator.Evaluator = (*mocks.LabelEvaluator)(nil)
	_ labelresolver.Resolver   = (*mocks.LabelResolver)(nil)
	_ store.Interface          = (*mocks.Store)(nil)
	_ controller.Runner        = (*mocks.Runtime)(nil)
)
