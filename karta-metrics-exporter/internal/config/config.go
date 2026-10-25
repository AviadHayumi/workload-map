// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package config loads the exporter configuration from a YAML file, flags and
// environment variables.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/controller"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
)

// Loader resolves the configuration from the file, environment variables and flags.
type Loader struct {
	flags     map[string]*string
	lookupEnv func(string) (string, bool)
	flagSet   *flag.FlagSet
}

// Config is the exporter configuration. It is read from the YAML file named by
// --config. A flag, or its environment variable, overrides the file when set.
type Config struct {
	ResourceRules         []controller.ResourceRule `json:"resourceRules"`
	ExcludeResourceRules  []controller.ResourceRule `json:"excludeResourceRules"`
	NamespaceSelector     *metav1.LabelSelector     `json:"namespaceSelector"`
	ObjectSelector        *metav1.LabelSelector     `json:"objectSelector"`
	PodSelector           *metav1.LabelSelector     `json:"podSelector"`
	KartaDefinitionSource inventory.Config          `json:"kartaDefinitionSource"`
	IgnoredOwnerKinds     Kinds                     `json:"ignoredOwnerKinds"`
	RequiredKinds         Kinds                     `json:"requiredKinds"`
	RefreshInterval       string                    `json:"refreshInterval"`
	MaxAge                string                    `json:"maxAge"`
	RequestTimeout        string                    `json:"requestTimeout"`
	APIQPS                float32                   `json:"apiQPS"`
	APIBurst              int                       `json:"apiBurst"`
	Workers               int                       `json:"workers"`
	LabelRules            []labelresolver.LabelRule `yaml:"labelRules" json:"labelRules"`
	LabelLimits           labelevaluator.Limits     `yaml:"labelLimits" json:"labelLimits"`
}

// setting is one configuration value with its flag and environment variable.
// apply parses the raw value into the label limits.
type setting struct {
	flag, env, usage string
	apply            func(*labelevaluator.Limits, string) error
	applyConfig      func(*Config, string) error
}

const (
	flagConfig                  = "config"
	flagCostLimit               = "label-cost-limit"
	flagMaxExpressionBytes      = "label-max-expression-bytes"
	flagMaxValueBytes           = "label-max-value-bytes"
	flagInterruptCheckFrequency = "label-interrupt-check-frequency"
)

// errZeroInterruptCheckFrequency rejects a setting that would stop CEL from
// checking for cancellation during evaluation.
var errZeroInterruptCheckFrequency = errors.New("labelLimits.interruptCheckFrequency must be greater than 0")

var errMultipleDocuments = errors.New("must contain a single YAML document")

var defaultLabelLimits = labelevaluator.Limits{
	CostLimit:               labelevaluator.DefaultCostLimit,
	MaxExpressionBytes:      labelevaluator.DefaultMaxExpressionBytes,
	MaxValueBytes:           labelevaluator.DefaultMaxValueBytes,
	InterruptCheckFrequency: labelevaluator.DefaultInterruptCheckFrequency,
}

var configSetting = setting{flag: flagConfig, env: "KARTA_EXPORTER_CONFIG", usage: "Path to the exporter YAML config file."}

var labelSettings = []setting{
	{
		flag: flagCostLimit,
		env:  "KARTA_EXPORTER_LABEL_COST_LIMIT",
		usage: fmt.Sprintf("CEL cost units one label evaluation may spend (default %d). Overrides labelLimits.costLimit.",
			defaultLabelLimits.CostLimit),
		apply: func(limits *labelevaluator.Limits, v string) (err error) {
			limits.CostLimit, err = strconv.ParseUint(v, 10, 64)
			return err
		},
	},
	{
		flag: flagMaxExpressionBytes,
		env:  "KARTA_EXPORTER_LABEL_MAX_EXPRESSION_BYTES",
		usage: fmt.Sprintf("Maximum length of the label expression (default %d). Overrides labelLimits.maxExpressionBytes.",
			defaultLabelLimits.MaxExpressionBytes),
		apply: func(limits *labelevaluator.Limits, v string) (err error) {
			limits.MaxExpressionBytes, err = strconv.Atoi(v)
			return err
		},
	},
	{
		flag: flagMaxValueBytes,
		env:  "KARTA_EXPORTER_LABEL_MAX_VALUE_BYTES",
		usage: fmt.Sprintf("Maximum length of an extracted label value (default %d). Overrides labelLimits.maxValueBytes.",
			defaultLabelLimits.MaxValueBytes),
		apply: func(limits *labelevaluator.Limits, v string) (err error) {
			limits.MaxValueBytes, err = strconv.Atoi(v)
			return err
		},
	},
	{
		flag: flagInterruptCheckFrequency,
		env:  "KARTA_EXPORTER_LABEL_INTERRUPT_CHECK_FREQUENCY",
		usage: fmt.Sprintf("Comprehension iterations between cancellation checks; must be greater than 0 (default %d). Overrides labelLimits.interruptCheckFrequency.",
			defaultLabelLimits.InterruptCheckFrequency),
		apply: func(limits *labelevaluator.Limits, v string) error {
			n, err := strconv.ParseUint(v, 10, 0)
			limits.InterruptCheckFrequency = uint(n)
			return err
		},
	},
}

var runtimeSettings = []setting{
	{flag: "api-qps", env: "KARTA_EXPORTER_API_QPS", usage: "Shared Kubernetes API request rate (default 20).", applyConfig: func(c *Config, v string) error { n, e := strconv.ParseFloat(v, 32); c.APIQPS = float32(n); return e }},
	{flag: "api-burst", env: "KARTA_EXPORTER_API_BURST", usage: "Shared Kubernetes API request burst (default 40).", applyConfig: func(c *Config, v string) error { n, e := strconv.Atoi(v); c.APIBurst = n; return e }},
	{flag: "workers", env: "KARTA_EXPORTER_WORKERS", usage: "Workers per object resolution and root publication queue (default 2).", applyConfig: func(c *Config, v string) error { n, e := strconv.Atoi(v); c.Workers = n; return e }},
	{flag: "refresh-interval", env: "KARTA_EXPORTER_REFRESH_INTERVAL", usage: "Interval between source and ownership proof refreshes (default 45s).", applyConfig: func(c *Config, v string) error { c.RefreshInterval = v; return nil }},
	{flag: "max-age", env: "KARTA_EXPORTER_MAX_AGE", usage: "Maximum source and ownership proof age (default 2m).", applyConfig: func(c *Config, v string) error { c.MaxAge = v; return nil }},
	{flag: "request-timeout", env: "KARTA_EXPORTER_REQUEST_TIMEOUT", usage: "Timeout per metadata GET or LIST page (default 30s).", applyConfig: func(c *Config, v string) error { c.RequestTimeout = v; return nil }},
}

// NewLoader registers the configuration flags on fs. Each flag can also be set
// from its KARTA_EXPORTER_* environment variable, read with lookupEnv.
func NewLoader(fs *flag.FlagSet, lookupEnv func(string) (string, bool)) *Loader {
	l := &Loader{flags: map[string]*string{}, lookupEnv: lookupEnv, flagSet: fs}
	for _, s := range append(append([]setting{configSetting}, labelSettings...), runtimeSettings...) {
		l.flags[s.flag] = fs.String(s.flag, "", fmt.Sprintf("%s Environment variable: %s.", s.usage, s.env))
	}
	return l
}

// Load reads the config file, when set, and applies the environment variables
// and then the flags, so a flag wins. Explicit flags win even when empty. Unknown keys
// in the file are an error. Call it after fs is parsed.
func (l *Loader) Load() (Config, error) {
	cfg := DefaultConfig()

	if path, _ := l.value(configSetting); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config file %s: %w", path, err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		var document yaml.Node
		// An empty file decodes to io.EOF and keeps the defaults.
		switch err := dec.Decode(&document); {
		case errors.Is(err, io.EOF):
		case err != nil:
			return Config{}, fmt.Errorf("config file %s: %w", path, err)
		default:
			encoded, err := yaml.Marshal(&document)
			if err != nil {
				return Config{}, err
			}
			jsonData, err := sigyaml.YAMLToJSONStrict(encoded)
			if err != nil {
				return Config{}, err
			}
			jsonDecoder := json.NewDecoder(bytes.NewReader(jsonData))
			jsonDecoder.DisallowUnknownFields()
			if err := jsonDecoder.Decode(&cfg); err != nil {
				return Config{}, fmt.Errorf("config file %s: %w", path, err)
			}
			// A trailing "---" yields an empty document, which is not a second config.
			for {
				var extra any
				err := dec.Decode(&extra)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil || extra != nil {
					return Config{}, fmt.Errorf("config file %s: %w", path, errMultipleDocuments)
				}
			}
		}
	}

	for _, s := range labelSettings {
		if v, set := l.value(s); set {
			if err := s.apply(&cfg.LabelLimits, v); err != nil {
				return Config{}, fmt.Errorf("--%s (%s): %w", s.flag, s.env, err)
			}
		}
	}

	for _, s := range runtimeSettings {
		if v, set := l.value(s); set {
			if err := s.applyConfig(&cfg, v); err != nil {
				return Config{}, fmt.Errorf("--%s (%s): %w", s.flag, s.env, err)
			}
		}
	}
	if cfg.LabelLimits.InterruptCheckFrequency == 0 {
		return Config{}, errZeroInterruptCheckFrequency
	}
	if cfg.LabelLimits.MaxExpressionBytes < 0 || cfg.LabelLimits.MaxValueBytes < 0 {
		return Config{}, labelevaluator.ErrNegativeLimit
	}
	if err := labelresolver.ValidateConfig(labelresolver.Config{Rules: cfg.LabelRules, Limits: cfg.LabelLimits}); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// value distinguishes an explicit empty flag from an unset one.
func (l *Loader) value(s setting) (string, bool) {
	set := false
	l.flagSet.Visit(func(f *flag.Flag) {
		if f.Name == s.flag {
			set = true
		}
	})
	if set {
		return *l.flags[s.flag], true
	}
	return l.lookupEnv(s.env)
}

// DefaultConfig selects cluster definitions and bounds evidence age and worker concurrency.
func DefaultConfig() Config {
	return Config{LabelLimits: defaultLabelLimits, KartaDefinitionSource: inventory.Config{Catalog: inventory.CatalogConfig{Exclude: []string{"core-pod-v1"}}, Cluster: inventory.ClusterConfig{Enabled: true}}, RefreshInterval: "45s", MaxAge: "2m", RequestTimeout: "30s", Workers: 2, APIQPS: 20, APIBurst: 40}
}

// Validate rejects runtime settings before any clients start.
func (cfg Config) Validate() error {
	for _, selector := range []*metav1.LabelSelector{cfg.NamespaceSelector, cfg.ObjectSelector, cfg.PodSelector, cfg.KartaDefinitionSource.Cluster.LabelSelector} {
		if selector != nil {
			if _, err := metav1.LabelSelectorAsSelector(selector); err != nil {
				return err
			}
		}
	}
	if err := controller.ValidateResourceRules(append(append([]controller.ResourceRule{}, cfg.ResourceRules...), cfg.ExcludeResourceRules...)); err != nil {
		return err
	}
	for _, kinds := range [][]schema.GroupVersionKind{cfg.IgnoredOwnerKinds, cfg.RequiredKinds} {
		seen := map[schema.GroupVersionKind]bool{}
		for _, kind := range kinds {
			if kind.Kind == "" || kind.Version == "" || seen[kind] {
				return fmt.Errorf("invalid or repeated kind %s", kind)
			}
			seen[kind] = true
		}
	}
	refresh, err := time.ParseDuration(cfg.RefreshInterval)
	if err != nil || refresh <= 0 {
		return errors.New("refreshInterval must be a positive duration")
	}
	maxAge, err := time.ParseDuration(cfg.MaxAge)
	if err != nil || maxAge <= refresh {
		return errors.New("maxAge must exceed refreshInterval")
	}
	timeout, err := time.ParseDuration(cfg.RequestTimeout)
	if err != nil || timeout <= 0 {
		return errors.New("requestTimeout must be a positive duration")
	}
	if cfg.APIQPS <= 0 || math.IsNaN(float64(cfg.APIQPS)) || math.IsInf(float64(cfg.APIQPS), 0) {
		return errors.New("apiQPS must be finite and positive")
	}
	if cfg.APIBurst <= 0 {
		return errors.New("apiBurst must be positive")
	}
	if cfg.Workers <= 0 {
		return errors.New("workers must be positive")
	}
	return nil
}
