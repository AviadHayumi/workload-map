// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelresolver

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
)

type resolver struct{ rules map[string][]compiledRule }

type compiledRule struct {
	name      string
	sources   []Source
	evaluator labelevaluator.Evaluator
}

// New validates and compiles rules once. The returned resolver supports concurrent
// calls. Callers must not mutate source objects while Resolve is evaluating them.
func New(cfg Config, deps Dependencies) (Resolver, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.Limits.MaxExpressionBytes < 0 || cfg.Limits.MaxValueBytes < 0 {
		return nil, labelevaluator.ErrNegativeLimit
	}
	if len(cfg.Rules) != 0 && deps.Compiler == nil {
		return nil, errors.New("label compiler is required")
	}
	r := &resolver{rules: make(map[string][]compiledRule)}
	for i, rule := range cfg.Rules {
		evaluator, err := deps.Compiler.Compile(labelevaluator.Config{Expression: rule.Expression, Limits: cfg.Limits})
		if err != nil {
			return nil, fmt.Errorf("labelRules[%d] %q: %w", i, rule.Name, err)
		}
		compiled := compiledRule{name: rule.Name, sources: slices.Clone(rule.Sources), evaluator: evaluator}
		for _, metric := range rule.Metrics {
			r.rules[metric] = append(r.rules[metric], compiled)
		}
	}
	return r, nil
}

func (r *resolver) Resolve(ctx context.Context, input Input) ([]ResolvedMetricLabels, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(input.Metrics))
	for _, metric := range input.Metrics {
		if metric != "karta_pod_workload_info" && metric != "karta_workload_status" {
			return nil, fmt.Errorf("unsupported label metric %q", metric)
		}
		if seen[metric] {
			return nil, fmt.Errorf("duplicate label metric %q", metric)
		}
		seen[metric] = true
	}
	results := make([]ResolvedMetricLabels, 0, len(input.Metrics))
	for _, metric := range input.Metrics {
		result := ResolvedMetricLabels{Metric: metric, Labels: make(map[string]string), Errors: make(map[string]error)}
		for _, rule := range r.rules[metric] {
			for _, source := range rule.sources {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				object := input.Workload
				if source == SourcePod {
					object = input.Pod
				}
				value, err := rule.evaluator.Evaluate(ctx, object)
				if cause := ctx.Err(); cause != nil {
					return nil, cause
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
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
		results = append(results, result)
	}
	return results, nil
}
