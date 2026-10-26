// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/controller"
	workloaddiscovery "github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/server"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/submitterextractor"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/telemetry"
)

type configuration struct {
	DefinitionSources    kartadefinitioninventory.Options `json:"kartaDefinitionSource"`
	NamespaceSelector    *metav1.LabelSelector            `json:"namespaceSelector,omitempty"`
	ObjectSelector       *metav1.LabelSelector            `json:"objectSelector,omitempty"`
	PodSelector          *metav1.LabelSelector            `json:"podSelector,omitempty"`
	ResourceRules        []controller.ResourceRule        `json:"resourceRules,omitempty"`
	ExcludeResourceRules []controller.ResourceRule        `json:"excludeResourceRules,omitempty"`
	IgnoredOwnerKinds    []string                         `json:"ignoredOwnerKinds,omitempty"`
	RequiredKinds        []string                         `json:"requiredKinds,omitempty"`
}

type commandOptions struct {
	config                    configuration
	address, kubeconfig, mode string
	metricOptions             collector.Options
	submitter                 submitterextractor.Interface
	ignored, required         []schema.GroupVersionKind
	otlp                      telemetry.OTLPConfig
}

func main() {
	var configPath, kubeconfig, address, mode, phases, identityPath, namespace, ignored, required string
	var useCatalog bool
	var otlpInterval, otlpTimeout time.Duration
	flag.StringVar(&configPath, "config", "", "Exporter JSON configuration file.")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Kubeconfig path; empty uses in-cluster configuration.")
	flag.StringVar(&address, "metrics-bind-address", ":8080", "Address for health endpoints and Prometheus metrics.")
	flag.StringVar(&mode, "metrics-exporter", os.Getenv("OTEL_METRICS_EXPORTER"), "Metric delivery: prometheus or otlp.")
	flag.StringVar(&phases, "workload-phases", "all", "Status phases: all, none, or a comma-separated list.")
	flag.StringVar(&identityPath, "identity-config", "", "JSON file containing submittedByExpression.")
	flag.StringVar(&namespace, "namespace", "", "Select one namespace by name.")
	flag.BoolVar(&useCatalog, "use-catalog", false, "Enable built-in catalog definitions.")
	flag.StringVar(&ignored, "ignored-owner-kinds", "", "Exact group/version/Kind owner boundaries.")
	flag.StringVar(&required, "required-kinds", "", "Exact group/version/Kind APIs required for readiness.")
	flag.DurationVar(&otlpInterval, "otlp-interval", 0, "OTLP export interval; overrides OTEL_METRIC_EXPORT_INTERVAL.")
	flag.DurationVar(&otlpTimeout, "otlp-timeout", 0, "OTLP export timeout; overrides OTEL_METRIC_EXPORT_TIMEOUT.")
	flag.Parse()
	var intervalSet, timeoutSet bool
	flag.Visit(func(value *flag.Flag) {
		intervalSet = intervalSet || value.Name == "otlp-interval"
		timeoutSet = timeoutSet || value.Name == "otlp-timeout"
	})
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	options, err := loadOptions(configPath, identityPath, mode, phases)
	if err != nil {
		logger.Error("invalid exporter configuration", "error", err)
		os.Exit(1)
	}
	options.address, options.kubeconfig = address, kubeconfig
	if useCatalog {
		options.config.DefinitionSources.Catalog.Enabled = true
	}
	if namespace != "" {
		options.config.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace}}
	}
	if ignored != "" {
		options.ignored, err = parseKinds(ignored)
		if err != nil {
			logger.Error("invalid ignored owner kinds", "error", err)
			os.Exit(1)
		}
	}
	if required != "" {
		options.required, err = parseKinds(required)
		if err != nil {
			logger.Error("invalid required kinds", "error", err)
			os.Exit(1)
		}
	}
	if intervalSet {
		options.otlp.Interval = otlpInterval
	}
	if timeoutSet {
		options.otlp.Timeout = otlpTimeout
	}
	if options.mode == "otlp" && (options.otlp.Interval <= 0 || options.otlp.Timeout <= 0 ||
		options.otlp.Interval%time.Millisecond != 0 || options.otlp.Timeout%time.Millisecond != 0) {
		logger.Error("OTLP interval and timeout must be positive whole-millisecond durations")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, options, logger); err != nil {
		logger.Error("exporter failed", "error", err)
		os.Exit(1)
	}
}

func loadOptions(configPath, identityPath, mode, phases string) (commandOptions, error) {
	options := commandOptions{config: configuration{DefinitionSources: kartadefinitioninventory.DefaultOptions()}}
	var err error
	options.mode, err = telemetry.ParseExporter(mode)
	if err != nil {
		return options, err
	}
	options.metricOptions, err = collector.ParseOptions(phases)
	if err != nil {
		return options, err
	}
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return options, err
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		config := &options.config
		if err := decoder.Decode(&config); err != nil {
			return options, fmt.Errorf("decode exporter config: %w", err)
		}
		if config == nil {
			return options, errors.New("exporter config must be a JSON object")
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return options, errors.New("exporter config must contain exactly one JSON object")
		}
	}
	for _, selector := range []*metav1.LabelSelector{options.config.NamespaceSelector, options.config.ObjectSelector, options.config.PodSelector} {
		if selector != nil {
			if _, err := metav1.LabelSelectorAsSelector(selector); err != nil {
				return options, err
			}
		}
	}
	for _, rules := range [][]controller.ResourceRule{options.config.ResourceRules, options.config.ExcludeResourceRules} {
		if err := controller.ValidateResourceRules(rules); err != nil {
			return options, err
		}
	}
	if _, err := kartadefinitioninventory.NewSnapshot(options.config.DefinitionSources, nil); err != nil {
		return options, fmt.Errorf("validate definition sources: %w", err)
	}
	options.ignored, err = parseKinds(strings.Join(options.config.IgnoredOwnerKinds, ","))
	if err != nil {
		return options, err
	}
	options.required, err = parseKinds(strings.Join(options.config.RequiredKinds, ","))
	if err != nil {
		return options, err
	}
	if identityPath != "" {
		data, err := os.ReadFile(identityPath)
		if err != nil {
			return options, err
		}
		options.submitter, err = submitterextractor.Parse(data)
		if err != nil {
			return options, err
		}
		options.metricOptions.Identity = true
	}
	if options.mode == "otlp" {
		options.otlp, err = telemetry.OTLPConfigFromEnv()
		if err != nil {
			return options, err
		}
	}
	return options, nil
}

func run(ctx context.Context, options commandOptions, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	restConfig, err := buildRESTConfig(options.kubeconfig)
	if err != nil {
		return fmt.Errorf("build REST config: %w", err)
	}
	restConfig.QPS, restConfig.Burst = 50, 100
	// WATCH connections live until their server timeout or generation stops.
	// Individual LIST calls have controller-owned request deadlines.
	restConfig.Timeout = 0
	client, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	metadataClient, err := metadata.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	discoveryConfig := rest.CopyConfig(restConfig)
	discoveryConfig.Timeout = 30 * time.Second
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(discoveryConfig)
	if err != nil {
		return err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))
	inventory, err := kartadefinitioninventory.New(client, options.config.DefinitionSources)
	if err != nil {
		return err
	}
	reader := controller.NewInformerReader()
	discovery := workloaddiscovery.New(reader, inventory, options.ignored...)
	metricsStore := store.New()
	runtime, err := controller.NewRuntime(client, metadataClient, mapper, reader, discovery, inventory,
		options.submitter, metricsStore, controller.RuntimeOptions{
			NamespaceSelector: options.config.NamespaceSelector, ObjectSelector: options.config.ObjectSelector,
			PodSelector: options.config.PodSelector, ResourceRules: options.config.ResourceRules,
			ExcludeResourceRules: options.config.ExcludeResourceRules, Definitions: options.config.DefinitionSources,
			RequiredKinds: options.required,
		})
	if err != nil {
		return err
	}
	var provider *sdkmetric.MeterProvider
	var gatherer prometheus.Gatherer
	if options.mode == "otlp" {
		provider, err = telemetry.NewOTLPProvider(ctx, options.otlp)
	} else {
		registry := prometheus.NewRegistry()
		registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
		gatherer = registry
		provider, err = telemetry.NewPrometheusProvider(registry)
	}
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
	registration, err := collector.New(provider.Meter("karta-exporter"), metricsStore, options.metricOptions)
	if err != nil {
		return fmt.Errorf("register workload metrics: %w", err)
	}
	defer func() {
		if err := registration.Unregister(); err != nil {
			logger.Error("unregister workload metrics", "error", err)
		}
	}()
	httpServer := server.New(options.address, gatherer, runtime.Ready)
	results := make(chan error, 3)
	go func() { results <- inventory.Run(ctx) }()
	go func() { results <- runtime.Run(ctx) }()
	go func() { results <- httpServer.Run(ctx) }()
	logger.Info("karta exporter started", "metricsAddr", options.address, "delivery", options.mode)
	select {
	case result := <-results:
		cancel()
		return errors.Join(result, <-results, <-results)
	case <-ctx.Done():
		return errors.Join(<-results, <-results, <-results)
	}
}

func buildRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

func parseKinds(value string) ([]schema.GroupVersionKind, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var kinds []schema.GroupVersionKind
	seen := make(map[schema.GroupVersionKind]bool)
	for _, value := range strings.Split(value, ",") {
		value = strings.TrimSpace(value)
		parts := strings.Split(value, "/")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("invalid kind %q: expected group/version/Kind or version/Kind", value)
		}
		for _, part := range parts {
			if part == "" || strings.ContainsAny(part, " \t\r\n*") {
				return nil, fmt.Errorf("invalid kind %q: every segment must be nonempty and exact", value)
			}
		}
		gv, err := schema.ParseGroupVersion(strings.Join(parts[:len(parts)-1], "/"))
		if err != nil {
			return nil, fmt.Errorf("invalid kind %q: %w", value, err)
		}
		gvk := gv.WithKind(parts[len(parts)-1])
		if !seen[gvk] {
			kinds = append(kinds, gvk)
			seen[gvk] = true
		}
	}
	return kinds, nil
}
