// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/otlptranslator"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	collector "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/adapters/otel"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/controller"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Application owns Kubernetes workers and the HTTP/OTel lifecycle.
type Application struct {
	Components
	Handler  http.Handler
	provider *sdkmetric.MeterProvider
}

// Components supplies the workers and their shared publication store.
type Components struct {
	Runtime     controller.Runner
	Store       store.Interface
	Definitions DefinitionRunner
}

// DefinitionRunner maintains the Karta inventory until cancellation.
type DefinitionRunner interface {
	Run(context.Context) error
}

func New(components Components) (*Application, error) {
	if components.Runtime == nil || components.Store == nil || components.Definitions == nil {
		return nil, errors.New("incomplete exporter components")
	}
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry), otelprom.WithTranslationStrategy(otlptranslator.NoTranslation), otelprom.WithoutScopeInfo(), otelprom.WithoutTargetInfo())
	if err != nil {
		return nil, err
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter), sdkmetric.WithCardinalityLimit(0))
	application := &Application{Components: components, provider: provider}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{MaxRequestsInFlight: 1}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !application.Runtime.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	application.Handler = mux
	return application, nil
}

// Serve stops workers before unregistering collection and shutting down OTel.
func (a *Application) Serve(ctx context.Context, address string) (err error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for metrics: %w", err)
	}
	return a.ServeListener(ctx, listener)
}

// ServeListener serves an already bound listener and owns its shutdown.
func (a *Application) ServeListener(ctx context.Context, listener net.Listener) (err error) {
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		err = errors.Join(err, a.provider.Shutdown(shutdownCtx))
	}()
	registration, err := collector.Register(a.provider.Meter("karta-metrics-exporter"), a.Store)
	if err != nil {
		return errors.Join(fmt.Errorf("register collector: %w", err), listener.Close())
	}
	defer func() { err = errors.Join(err, registration.Unregister()) }()

	child, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second}
	failures := make(chan error, 3)
	var workers sync.WaitGroup
	workers.Go(func() { failures <- a.Definitions.Run(child) })
	workers.Go(func() { failures <- a.Runtime.Run(child) })
	workers.Go(func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- err
		}
	})
	select {
	case <-ctx.Done():
	case err = <-failures:
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	err = errors.Join(err, server.Shutdown(shutdownCtx))
	workers.Wait()
	close(failures)
	for failure := range failures {
		err = errors.Join(err, failure)
	}
	return err
}
