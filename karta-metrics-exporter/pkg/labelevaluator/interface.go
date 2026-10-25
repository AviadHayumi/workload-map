// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelevaluator

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Evaluator interface {
	Evaluate(context.Context, *unstructured.Unstructured) (string, error)
}

type Compiler interface {
	Compile(Config) (Evaluator, error)
}

type Config struct {
	Expression string
	Limits     Limits
}

// Limits bounds one expression. Zero selects the compiler default.
type Limits struct {
	CostLimit               uint64 `yaml:"costLimit" json:"costLimit"`
	MaxExpressionBytes      int    `yaml:"maxExpressionBytes" json:"maxExpressionBytes"`
	MaxValueBytes           int    `yaml:"maxValueBytes" json:"maxValueBytes"`
	InterruptCheckFrequency uint   `yaml:"interruptCheckFrequency" json:"interruptCheckFrequency"`
}
