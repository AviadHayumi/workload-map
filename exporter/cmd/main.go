// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/controller"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/server"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/telemetry"
)

func main() {
	var (
		metricsAddr     string
		metricsExporter string
		kubeconfig      string
		fullPodCache    bool
		useCatalog      bool
		resync          time.Duration
		workloadPhases  string
		podPhases       string
		podMetricsMode  string
		namespace       string
		requiredKinds   string
		healthInterval  time.Duration
		healthMaxAge    time.Duration
	)
	flag.StringVar(&metricsExporter, "metrics-exporter", os.Getenv("OTEL_METRICS_EXPORTER"), "Metric delivery: prometheus (default). Overrides OTEL_METRICS_EXPORTER; other outputs are not supported yet.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address to serve /metrics, /healthz, and /readyz on.")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig; empty means in-cluster configuration.")
	flag.BoolVar(&fullPodCache, "full-pod-cache", false, "Cache full pod objects instead of the trimmed shape. Needed only for custom Kartas whose pod selectors read fields outside metadata, spec.nodeName, and status.phase.")
	flag.BoolVar(&useCatalog, "use-catalog", false, "Use the built-in catalog definitions for workload kinds that have no Karta CR on the cluster. A Karta CR always overrides the catalog.")
	flag.DurationVar(&resync, "resync-period", 10*time.Hour, "Informer resync period.")
	flag.StringVar(&workloadPhases, "workload-phases", "all", "Normalized states to emit: all, none, or a comma-separated list. Selected states retain dense 0/1 samples.")
	flag.StringVar(&podPhases, "pod-phases", "all", "Component Pod count phases to emit: all, none, or a comma-separated list.")
	flag.StringVar(&podMetricsMode, "pod-metrics-mode", collector.PodMetricsCounts, "Pod metrics: counts (default), inventory (phase on attribution plus component inventory), or both (comparison mode).")
	flag.StringVar(&namespace, "namespace", "", "Observe one namespace; empty observes all namespaces. Karta definitions remain cluster scoped.")
	flag.StringVar(&requiredKinds, "required-kinds", "", "Required kinds even before discovery: comma-separated group/version/Kind (core: v1/Pod).")
	flag.DurationVar(&healthInterval, "health-check-interval", 30*time.Second, "Interval between fresh API and attribution verification passes.")
	flag.DurationVar(&healthMaxAge, "health-max-age", 2*time.Minute, "Maximum age of a successful verification; must exceed twice the check interval.")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	metricsExporter, err := telemetry.ParseExporter(metricsExporter)
	if err != nil {
		logger.Error("invalid metric delivery", "error", err)
		os.Exit(1)
	}
	metricOptions, err := collector.ParseOptions(workloadPhases, podPhases, podMetricsMode)
	if err != nil {
		logger.Error("invalid metric selection", "error", err)
		os.Exit(1)
	}
	logger.Info("metric delivery configured", "exporter", metricsExporter)
	controllerOptions := controller.Options{FullPodCache: fullPodCache, Resync: resync, Namespace: namespace, HealthInterval: healthInterval, HealthMaxAge: healthMaxAge}
	for _, kind := range strings.Split(requiredKinds, ",") {
		if kind == "" {
			continue
		}
		parts := strings.Split(kind, "/")
		if len(parts) < 2 || len(parts) > 3 || parts[len(parts)-1] == "" {
			logger.Error("invalid required kind", "kind", kind)
			os.Exit(1)
		}
		gv, err := schema.ParseGroupVersion(strings.Join(parts[:len(parts)-1], "/"))
		if err != nil {
			logger.Error("invalid required kind", "error", err)
			os.Exit(1)
		}
		controllerOptions.RequiredKinds = append(controllerOptions.RequiredKinds, gv.WithKind(parts[len(parts)-1]))
	}
	if err := run(metricsAddr, kubeconfig, useCatalog, controllerOptions, metricOptions, logger); err != nil {
		logger.Error("exporter failed", "error", err)
		os.Exit(1)
	}
}

func run(metricsAddr, kubeconfig string, useCatalog bool, controllerOptions controller.Options, metricOptions collector.Options, logger *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	restConfig, err := buildRESTConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to build rest config: %w", err)
	}

	restConfig.Timeout = 30 * time.Second
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}
	metadataClient, err := metadata.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create metadata client: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))

	kartaRegistry := registry.New()
	if useCatalog {
		kartaRegistry.SeedCatalog()
	}
	ownerIndex := owner.New()
	metricsStore := store.New()

	prometheusRegistry := prometheus.NewRegistry()
	prometheusRegistry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	provider, err := telemetry.NewPrometheusProvider(prometheusRegistry)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			logger.Error("metrics shutdown failed", "error", err)
		}
	}()
	meter := provider.Meter("karta-exporter")

	exporterController, err := controller.New(
		dynamicClient, metadataClient, kubeClient, mapper,
		kartaRegistry, ownerIndex, metricsStore,
		meter, logger,
		controllerOptions,
	)

	if err != nil {
		return err
	}

	metricOptions.PendingPods = ownerIndex.PendingCount
	metricOptions.KartaCounts = func() (int, int, int) {
		stats := kartaRegistry.Stats()
		return stats.Valid, stats.Invalid, stats.Shadowed
	}
	registration, err := collector.New(meter, metricsStore, metricOptions)
	if err != nil {
		return fmt.Errorf("register workload observations: %w", err)
	}
	defer func() {
		if err := registration.Unregister(); err != nil {
			logger.Error("unregister workload observations failed", "error", err)
		}
	}()

	metricsServer := server.New(metricsAddr, prometheusRegistry, exporterController.Ready)

	errCh := make(chan error, 2)
	go func() { errCh <- exporterController.Run(ctx) }()
	go func() { errCh <- metricsServer.Run(ctx) }()

	logger.Info("karta exporter started", "metricsAddr", metricsAddr, "fullPodCache", controllerOptions.FullPodCache, "useCatalog", useCatalog)

	select {
	case err := <-errCh:
		cancel()
		otherErr := <-errCh
		return errors.Join(err, otherErr)
	case <-ctx.Done():
		return errors.Join(<-errCh, <-errCh)
	}
}

func buildRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
}
