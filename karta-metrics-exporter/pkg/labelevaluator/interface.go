// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelevaluator

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Evaluator evaluates one compiled expression against one full object.
type Evaluator interface {
	Evaluate(context.Context, *unstructured.Unstructured) (string, error)
}

type Config struct {
	Expression string
}

type CompileFunc func(Config) (Evaluator, error)
