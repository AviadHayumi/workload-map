// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelevaluator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// DefaultMaxExpressionBytes bounds the configured expression before compilation.
	DefaultMaxExpressionBytes = 4096
	// DefaultCostLimit bounds the CEL cost units spent on one evaluation.
	DefaultCostLimit = 10000
	// DefaultMaxValueBytes bounds the value a single evaluation may return.
	DefaultMaxValueBytes = 1024
	// DefaultInterruptCheckFrequency is how many comprehension iterations pass
	// between context cancellation checks.
	DefaultInterruptCheckFrequency = 100
)

var (
	ErrMissingObject = errors.New("label input object is nil")
	// ErrValueTooLong is returned when an expression result exceeds Config.Limits.MaxValueBytes.
	ErrValueTooLong = errors.New("label value exceeds the maximum length")
	// ErrNotString is returned when an expression does not produce a string.
	ErrNotString = errors.New("label expression must produce a string")
	// ErrExpressionTooLong is returned when the expression exceeds Config.Limits.MaxExpressionBytes.
	ErrExpressionTooLong = errors.New("label expression exceeds the maximum length")
	// ErrNegativeLimit is returned when a Config limit is negative.
	ErrNegativeLimit = errors.New("label limits must not be negative")
)

type celEvaluator struct {
	program       cel.Program
	maxValueBytes int
}

type compiler struct{}

type disabledEvaluator struct{}

func NewCompiler() Compiler { return compiler{} }

// Compile compiles the CEL expression once and returns an evaluator for it.
// An empty expression returns an empty value. Dynamic result types are checked
// during evaluation. Invalid expressions and negative limits are rejected.
func (compiler) Compile(cfg Config) (Evaluator, error) {
	limits := cfg.Limits
	if limits.MaxExpressionBytes < 0 || limits.MaxValueBytes < 0 {
		return nil, ErrNegativeLimit
	}
	if limits.CostLimit == 0 {
		limits.CostLimit = DefaultCostLimit
	}
	if limits.MaxExpressionBytes == 0 {
		limits.MaxExpressionBytes = DefaultMaxExpressionBytes
	}
	if limits.MaxValueBytes == 0 {
		limits.MaxValueBytes = DefaultMaxValueBytes
	}
	// cel-go disables cancellation checks during evaluation at zero.
	if limits.InterruptCheckFrequency == 0 {
		limits.InterruptCheckFrequency = DefaultInterruptCheckFrequency
	}

	expression := cfg.Expression
	if strings.TrimSpace(expression) == "" {
		return disabledEvaluator{}, nil
	}
	if len(expression) > limits.MaxExpressionBytes {
		return nil, fmt.Errorf("%w (%d bytes)", ErrExpressionTooLong, limits.MaxExpressionBytes)
	}

	env, err := cel.NewEnv(cel.Variable("object", cel.DynType))
	if err != nil {
		return nil, fmt.Errorf("creating CEL environment: %w", err)
	}

	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compiling label expression: %w", issues.Err())
	}
	if !ast.OutputType().IsExactType(cel.StringType) && !ast.OutputType().IsExactType(cel.DynType) {
		return nil, fmt.Errorf("%w, got %s", ErrNotString, ast.OutputType())
	}

	program, err := env.Program(ast,
		cel.CostLimit(limits.CostLimit),
		cel.InterruptCheckFrequency(limits.InterruptCheckFrequency),
	)
	if err != nil {
		return nil, fmt.Errorf("building label program: %w", err)
	}
	return &celEvaluator{program: program, maxValueBytes: limits.MaxValueBytes}, nil
}

func (disabledEvaluator) Evaluate(ctx context.Context, _ *unstructured.Unstructured) (string, error) {
	return "", ctx.Err()
}

func (e *celEvaluator) Evaluate(ctx context.Context, obj *unstructured.Unstructured) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if obj == nil {
		return "", ErrMissingObject
	}
	out, _, err := e.program.ContextEval(ctx, map[string]any{"object": obj.Object})
	if err != nil {
		// On interruption, ContextEval wraps the context cause, so errors.Is still matches it.
		return "", fmt.Errorf("evaluating label expression: %w", err)
	}

	value, ok := out.Value().(string)
	if !ok {
		return "", ErrNotString
	}
	if len(value) > e.maxValueBytes {
		return "", ErrValueTooLong
	}
	return value, nil
}
