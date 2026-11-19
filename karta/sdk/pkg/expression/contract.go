// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package expression defines the workload expression runner contract.
package expression

import (
	"context"
	"errors"
)

var ErrReferencesNotSupported = errors.New("reference evaluation requires supplied objects or a reader")

//go:generate go run go.uber.org/mock/mockgen -copyright_file=copyright.txt -source=contract.go -destination=runner_mock.go -package=expression Runner

type Evaluator interface {
	// Evaluate spreads a list result into separate entries.
	Evaluate(context.Context, string) ([]any, error)
	GetObject() (any, error)
}

type Assigner interface {
	// Assign replaces the root after the caller applies an atomic patch to a copy.
	Assign(context.Context, string, any) error
}

type VariableEvaluator interface {
	// EvaluateWritePath exposes instance and index, but never the written value.
	EvaluateWritePath(context.Context, string, map[string]any) ([]any, error)
	// EvaluateWithVariables preserves a list as a single result.
	EvaluateWithVariables(context.Context, string, map[string]any) ([]any, error)
	ResolveVariables(context.Context, ...string) (map[string]any, error)
}

type NamedExpression struct{ Name, Expression string }

type Runner interface {
	Evaluator
	Assigner
	VariableEvaluator
}
