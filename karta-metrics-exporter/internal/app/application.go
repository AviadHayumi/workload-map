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
	kubediscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/config"
	collector "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/adapters/otel"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/controller"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Application owns Kubernetes workers and the HTTP/OTel lifecycle.
type Application struct {
	Runtime  controller.Runner
	Store    store.Interface
	Handler  http.Handler
	provider *sdkmetric.MeterProvider
}

func New(cfg config.Config, kubeConfig *rest.Config) (*Application, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if kubeConfig == nil {
		return nil, errors.New("kubernetes configuration is required")
	}
	refresh, err := time.ParseDuration(cfg.RefreshInterval)
	if err != nil {
		return nil, fmt.Errorf("refreshInterval: %w", err)
	}
	maxAge, err := time.ParseDuration(cfg.MaxAge)
	if err != nil {
		return nil, fmt.Errorf("maxAge: %w", err)
	}
	timeout, err := time.ParseDuration(cfg.RequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("requestTimeout: %w", err)
	}
	kubeConfig = rest.CopyConfig(kubeConfig)
	kubeConfig.QPS = cfg.APIQPS
	kubeConfig.Burst = cfg.APIBurst
	kubeConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(cfg.APIQPS, cfg.APIBurst)
	dynamicClient, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		return nil, err
	}
	metadataClient, err := metadata.NewForConfig(kubeConfig)
	if err != nil {
		return nil, err
	}
	discoveryClient, err := kubediscovery.NewDiscoveryClientForConfig(kubeConfig)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))
	reader, err := controller.NewReader(controller.Clients{Dynamic: dynamicClient, Metadata: metadataClient, Mapper: mapper}, controller.ReaderOptions{TTL: maxAge, RequestTimeout: timeout})
	if err != nil {
		return nil, err
	}
	definitions, err := inventory.New(cfg.KartaDefinitionSource)
	if err != nil {
		return nil, err
	}
	ownership, err := discovery.New(reader, discovery.Config{IgnoredTopOwners: cfg.IgnoredOwnerKinds})
	if err != nil {
		return nil, err
	}
	labels, err := labelresolver.New(labelresolver.Config{Rules: cfg.LabelRules, Limits: cfg.LabelLimits})
	if err != nil {
		return nil, err
	}
	publications := store.New(store.Options{})
	runtime, err := controller.New(controller.Config{IgnoredOwnerKinds: cfg.IgnoredOwnerKinds, ResourceRules: cfg.ResourceRules, ExcludeResourceRules: cfg.ExcludeResourceRules, NamespaceSelector: cfg.NamespaceSelector, ObjectSelector: cfg.ObjectSelector, PodSelector: cfg.PodSelector, Definitions: cfg.KartaDefinitionSource, RequiredKinds: cfg.RequiredKinds, RefreshInterval: refresh, MaxAge: maxAge, Workers: cfg.Workers}, controller.Dependencies{Discovery: ownership, Inventory: definitions, Labels: labels, Publications: publications}, reader)
	if err != nil {
		return nil, err
	}
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry), otelprom.WithTranslationStrategy(otlptranslator.NoTranslation), otelprom.WithoutScopeInfo(), otelprom.WithoutTargetInfo())
	if err != nil {
		return nil, err
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter), sdkmetric.WithCardinalityLimit(0))
	application := &Application{Runtime: runtime, Store: publications, provider: provider}
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
	failures := make(chan error, 2)
	var workers sync.WaitGroup
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
