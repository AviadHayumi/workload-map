// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelresolver

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
)

// Resolver holds compiled label rules. ForWorkload may be called concurrently.
type Resolver struct {
	rules     map[string][]*compiledRule
	ruleCount int
}

type compiledRule struct {
	id       int
	name     string
	sources  []Source
	evaluate func(context.Context, *unstructured.Unstructured) (string, error)
}

// ResolveFunc evaluates labels for one metric and an optional Pod. Calls to the
// same function must be serialized. Each call returns independent result maps.
type ResolveFunc func(context.Context, string, *unstructured.Unstructured) (Result, error)

// Result holds values and evaluation errors keyed by label name.
type Result struct {
	Labels map[string]string
	Errors map[string]error
}

type Config struct {
	Rules  []LabelRule           `json:"labelRules" yaml:"labelRules"`
	Limits labelevaluator.Limits `json:"labelLimits" yaml:"labelLimits"`
}

type LabelRule struct {
	Name       string   `json:"name" yaml:"name"`
	Metrics    []string `json:"metrics" yaml:"metrics"`
	Sources    []Source `json:"sources" yaml:"sources"`
	Expression string   `json:"expression" yaml:"expression"`
}

type Source string

type evaluation struct {
	value string
	err   error
}

const (
	SourceWorkload Source = "workload"
	SourcePod      Source = "pod"
)

// New validates and compiles each rule once.
func New(cfg Config) (*Resolver, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.Limits.MaxExpressionBytes < 0 || cfg.Limits.MaxValueBytes < 0 {
		return nil, labelevaluator.ErrNegativeLimit
	}
	r := &Resolver{rules: make(map[string][]*compiledRule), ruleCount: len(cfg.Rules)}
	for i, rule := range cfg.Rules {
		evaluator, err := labelevaluator.Compile(labelevaluator.Config{Expression: rule.Expression, Limits: cfg.Limits})
		if err != nil {
			return nil, fmt.Errorf("labelRules[%d] %q: %w", i, rule.Name, err)
		}
		compiled := &compiledRule{id: i, name: rule.Name, sources: slices.Clone(rule.Sources), evaluate: evaluator.Evaluate}
		for _, metric := range rule.Metrics {
			r.rules[metric] = append(r.rules[metric], compiled)
		}
	}
	return r, nil
}

// ForWorkload prepares resolution for an immutable full workload object. Keep the
// returned function while that object remains current, including across Pod-only
// updates. Create a new function when the workload changes. Workload results are
// evaluated lazily and shared across metrics and Pods; cancellation is not cached.
func (r *Resolver) ForWorkload(workload *unstructured.Unstructured) ResolveFunc {
	cache := make([]*evaluation, r.ruleCount)
	return func(ctx context.Context, metric string, pod *unstructured.Unstructured) (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if metric != "karta_pod_workload_info" && metric != "karta_workload_status" {
			return Result{}, fmt.Errorf("unsupported label metric %q", metric)
		}
		result := Result{Labels: make(map[string]string), Errors: make(map[string]error)}
		for _, rule := range r.rules[metric] {
			for _, source := range rule.sources {
				if err := ctx.Err(); err != nil {
					return Result{}, err
				}
				var value string
				var err error
				switch {
				case source == SourcePod:
					value, err = rule.evaluate(ctx, pod)
				case cache[rule.id] != nil:
					value, err = cache[rule.id].value, cache[rule.id].err
				default:
					value, err = rule.evaluate(ctx, workload)
				}
				if cause := ctx.Err(); cause != nil {
					return Result{}, cause
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return Result{}, err
				}
				if source == SourceWorkload && cache[rule.id] == nil {
					cache[rule.id] = &evaluation{value: value, err: err}
				}
				if err != nil {
					result.Errors[rule.name] = errors.Join(result.Errors[rule.name], fmt.Errorf("%s: %w", source, err))
					continue
				}
				if value != "" {
					result.Labels[rule.name] = value
					break
				}
			}
		}
		return result, nil
	}
}
