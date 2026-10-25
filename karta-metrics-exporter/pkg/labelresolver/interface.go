// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelresolver

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
)

type Resolver interface {
	Resolve(context.Context, Input) ([]ResolvedMetricLabels, error)
}

type Input struct {
	Metrics  []string
	Workload *unstructured.Unstructured
	Pod      *unstructured.Unstructured
}

type ResolvedMetricLabels struct {
	Metric string
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

const (
	SourceWorkload Source = "workload"
	SourcePod      Source = "pod"
)

type Dependencies struct {
	Compiler labelevaluator.Compiler
}
