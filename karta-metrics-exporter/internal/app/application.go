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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
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
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

// Application owns Kubernetes workers and the HTTP/OTel lifecycle.
type Application struct {
	Components Components
	Runtime    *controller.Runtime
	Inventory  *inventory.Inventory
	Handler    http.Handler
	provider   *sdkmetric.MeterProvider
}

type kartaSource struct{ client dynamic.ResourceInterface }

func New(cfg config.Config, kubeConfig *rest.Config) (*Application, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if kubeConfig == nil {
		return nil, errors.New("Kubernetes configuration is required")
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
	definitions, err := inventory.New(cfg.KartaDefinitionSource, kartaSource{dynamicClient.Resource(v1alpha1.GroupVersion.WithResource("kartas"))})
	if err != nil {
		return nil, err
	}
	ownership, err := discovery.New(discovery.Dependencies{Reader: reader, Inventory: definitions}, discovery.Config{IgnoredTopOwners: cfg.IgnoredOwnerKinds})
	if err != nil {
		return nil, err
	}
	labels, err := labelresolver.New(labelresolver.Config{Rules: cfg.LabelRules, Limits: cfg.LabelLimits}, labelresolver.Dependencies{Compiler: labelevaluator.NewCompiler()})
	if err != nil {
		return nil, err
	}
	publications := store.New(store.Options{})
	runtime, err := controller.New(controller.Config{ResourceRules: cfg.ResourceRules, ExcludeResourceRules: cfg.ExcludeResourceRules, NamespaceSelector: cfg.NamespaceSelector, ObjectSelector: cfg.ObjectSelector, PodSelector: cfg.PodSelector, Definitions: cfg.KartaDefinitionSource, RequiredKinds: cfg.RequiredKinds, RefreshInterval: refresh, MaxAge: maxAge, Workers: cfg.Workers}, controller.Dependencies{Discovery: ownership, Inventory: definitions, Labels: labels, Publications: publications}, reader)
	if err != nil {
		return nil, err
	}
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry), otelprom.WithTranslationStrategy(otlptranslator.NoTranslation), otelprom.WithoutScopeInfo(), otelprom.WithoutTargetInfo())
	if err != nil {
		return nil, err
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter), sdkmetric.WithCardinalityLimit(0))
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{MaxRequestsInFlight: 1}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !runtime.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return &Application{Components: Components{Runtime: runtime, Store: publications, Registrar: collector.New()}, Runtime: runtime, Inventory: definitions, Handler: mux, provider: provider}, nil
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
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second}
	failures := make(chan error, 3)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := a.Inventory.Run(child); err != nil && child.Err() == nil {
			failures <- err
		}
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		failures <- Run(child, a.Components, a.provider.Meter("karta-metrics-exporter"))
	}()
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- err
		}
	}()
	select {
	case <-ctx.Done():
	case err = <-failures:
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	err = errors.Join(err, server.Shutdown(shutdownCtx))
	workers.Wait()
	<-served
	return errors.Join(err, a.provider.Shutdown(shutdownCtx))
}

func (s kartaSource) List(ctx context.Context, options metav1.ListOptions) (*v1alpha1.KartaList, error) {
	list, err := s.client.List(ctx, options)
	if err != nil {
		return nil, err
	}
	result := &v1alpha1.KartaList{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(list.UnstructuredContent(), result); err != nil {
		return nil, err
	}
	return result, nil
}
func (s kartaSource) Watch(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
	stream, err := s.client.Watch(ctx, options)
	if err != nil {
		return nil, err
	}
	return watch.Filter(stream, func(event watch.Event) (watch.Event, bool) {
		if event.Type == watch.Error || event.Type == watch.Bookmark {
			return event, true
		}
		body, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			return watch.Event{Type: watch.Error, Object: &metav1.Status{Status: metav1.StatusFailure, Message: fmt.Sprintf("unexpected Karta object %T", event.Object), Code: 500}}, true
		}
		result := &v1alpha1.Karta{}
		if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(body.Object, result); err != nil {
			return watch.Event{Type: watch.Error, Object: &metav1.Status{Status: metav1.StatusFailure, Message: err.Error(), Code: 500}}, true
		}
		event.Object = result
		return event, true
	}), nil
}
