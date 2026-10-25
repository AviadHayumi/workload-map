// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package scaffold

import (
	"context"
	"errors"

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
func New(labelConfig labelresolver.Config) app.Components {
	definitions := newInventory(inventory.Config{})
	reader := newObjectReader()
	ownership := newDiscovery(discovery.Dependencies{Reader: reader, Inventory: definitions})
	labels := newLabelResolver(labelConfig, labelresolver.Dependencies{Compiler: newCompiler()})
	publications := newStore(store.Options{})
	runtime := newRuntime(controller.Dependencies{
		Discovery: ownership, Inventory: definitions, Labels: labels,
		Publications: publications,
	})
	return app.Components{Runtime: runtime, Store: publications, Registrar: newRegistrar()}
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

func newCompiler() labelevaluator.Compiler {
	compiler := &mocks.LabelCompiler{}
	m := &mocks.LabelEvaluator{}
	m.EXPECT().Evaluate(mock.Anything, mock.Anything).Return("", ErrNotImplemented)
	compiler.EXPECT().Compile(mock.Anything).Return(m, nil)
	return compiler
}

func newLabelResolver(_ labelresolver.Config, _ labelresolver.Dependencies) labelresolver.Resolver {
	m := &mocks.LabelResolver{}
	m.EXPECT().Resolve(mock.Anything, mock.Anything).Return(nil, ErrNotImplemented)
	return m
}

func newStore(_ store.Options) store.Interface {
	m := &mocks.Store{}
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

func newRegistrar() collector.Registrar {
	m := &mocks.Registrar{}
	m.EXPECT().Register(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(meter metric.Meter, _ collector.Capturer, _ collector.Options) (metric.Registration, error) {
			return meter.RegisterCallback(func(context.Context, metric.Observer) error { return nil })
		})
	return m
}

// Keep generated mocks checked against their contracts even before implementations exist.
var (
	_ labelevaluator.Compiler  = (*mocks.LabelCompiler)(nil)
	_ collector.Registrar      = (*mocks.Registrar)(nil)
	_ inventory.Interface      = (*mocks.Inventory)(nil)
	_ discovery.Discoverer     = (*mocks.Discovery)(nil)
	_ discovery.ObjectReader   = (*mocks.ObjectReader)(nil)
	_ labelevaluator.Evaluator = (*mocks.LabelEvaluator)(nil)
	_ labelresolver.Resolver   = (*mocks.LabelResolver)(nil)
	_ store.Interface          = (*mocks.Store)(nil)
	_ controller.Runner        = (*mocks.Runtime)(nil)
)
