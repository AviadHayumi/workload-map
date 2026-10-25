// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelresolver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
)

type resolver struct {
	rules         map[string][]compiledRule
	mu            sync.Mutex
	workloadCache map[workloadKey]evaluation
	order         []workloadKey
	next          int
}

type workloadKey struct {
	apiVersion string
	kind       string
	uid        types.UID
	version    string
	rule       int
}

type evaluation struct {
	value string
	err   error
}

type compiledRule struct {
	id        int
	name      string
	sources   []Source
	evaluator labelevaluator.Evaluator
}

const workloadCacheSize = 1024

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
		compiled := compiledRule{id: i, name: rule.Name, sources: slices.Clone(rule.Sources), evaluator: evaluator}
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
				var value string
				var err error
				if source == SourcePod {
					value, err = rule.evaluator.Evaluate(ctx, input.Pod)
				} else {
					value, err = r.evaluateWorkload(ctx, rule, input.Workload)
				}
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

// Kubernetes objects with the same GVK, UID and resourceVersion have identical
// input data. Cache only workload results; each Pod still gets its own evaluation.
func (r *resolver) evaluateWorkload(ctx context.Context, rule compiledRule, object *unstructured.Unstructured) (string, error) {
	if object == nil || object.GetUID() == "" || object.GetResourceVersion() == "" {
		return rule.evaluator.Evaluate(ctx, object)
	}
	key := workloadKey{apiVersion: object.GetAPIVersion(), kind: object.GetKind(), uid: object.GetUID(), version: object.GetResourceVersion(), rule: rule.id}
	r.mu.Lock()
	cached, ok := r.workloadCache[key]
	r.mu.Unlock()
	if ok {
		return cached.value, cached.err
	}
	value, err := rule.evaluator.Evaluate(ctx, object)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return value, err
	}
	// Evaluation is deterministic for this immutable object and compiled rule.
	// Concurrent misses may evaluate independently; no CEL work holds the lock.
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.workloadCache[key]; !exists {
		if r.workloadCache == nil {
			r.workloadCache = make(map[workloadKey]evaluation)
		}
		if len(r.order) < workloadCacheSize {
			r.order = append(r.order, key)
		} else {
			delete(r.workloadCache, r.order[r.next])
			r.order[r.next] = key
			r.next = (r.next + 1) % workloadCacheSize
		}
		r.workloadCache[key] = evaluation{value: value, err: err}
	}
	return value, err
}
