// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cel

import (
	"context"
	"fmt"
	"strings"

	celgo "github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

// Program is a compiled CEL expression that can be evaluated concurrently.
type Program struct {
	program celgo.Program
}

const (
	maxExpressionBytes = 4096
	maxResultBytes     = 8192
	maxResultNodes     = 1024
	maxResultDepth     = 16
	maxCELCost         = 10000
)

// Compile builds a pure program with object and template as its only variables.
func Compile(expression string) (*Program, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	if len(expression) > maxExpressionBytes || strings.TrimSpace(expression) == "" {
		return nil, fmt.Errorf("CEL source must contain 1..%d bytes", maxExpressionBytes)
	}
	env, err := celgo.NewEnv(celgo.Variable("object", celgo.DynType), celgo.Variable("template", celgo.DynType))
	if err != nil {
		return nil, fmt.Errorf("create CEL environment: %w", err)
	}
	ast, issues := env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compile CEL source: %w", issues.Err())
	}
	if _, issues := env.Check(ast); issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("check CEL source: %w", issues.Err())
	}
	// cel-go v0.30.0 can narrow indexed dyn values to the wrong conversion overload.
	// Validate statically, then plan the parsed AST so runtime types select overloads.
	program, err := env.Program(ast, celgo.CostLimit(maxCELCost), celgo.InterruptCheckFrequency(100))
	if err != nil {
		return nil, fmt.Errorf("build CEL program: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Program{program: program}, nil
}

// Evaluate returns a detached string-keyed JSON map. CEL integers remain int64.
func (p *Program) Evaluate(ctx context.Context, input *Input) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.program == nil || input == nil || input.object == nil || input.template == nil {
		return nil, fmt.Errorf("CEL program or input is nil")
	}
	output, _, err := p.program.ContextEval(ctx, map[string]any{"object": input.object, "template": input.template})
	if err != nil {
		return nil, fmt.Errorf("evaluate CEL program: %w", err)
	}
	if _, ok := output.(traits.Mapper); !ok {
		return nil, fmt.Errorf("CEL result must be a string-keyed map")
	}
	budget := &jsonBudget{maxBytes: maxResultBytes, maxNodes: maxResultNodes, maxDepth: maxResultDepth}
	result, err := convertCEL(ctx, output, 1, budget)
	if err != nil {
		return nil, fmt.Errorf("convert CEL result: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func convertCEL(ctx context.Context, value ref.Val, depth int, budget *jsonBudget) (any, error) {
	if err := budget.visit(ctx, depth); err != nil {
		return nil, err
	}
	var scalar any
	switch value := value.(type) {
	case types.Null:
		scalar = nil
	case types.Bool:
		scalar = bool(value)
	case types.String:
		scalar = string(value)
	case types.Int:
		scalar = int64(value)
	case types.Double:
		scalar = float64(value)
	case traits.Mapper:
		size, ok := value.Size().(types.Int)
		if !ok || size < 0 || int64(size) > int64((budget.maxNodes-budget.nodes)/2) {
			return nil, fmt.Errorf("CEL map exceeds node limit")
		}
		encodedSize := 2
		if size > 0 {
			encodedSize += 2*int(size) - 1
		}
		if err := budget.addBytes(encodedSize); err != nil {
			return nil, err
		}
		result := make(map[string]any, int(size))
		iterator := value.Iterator()
		for iterator.HasNext() == types.True {
			key := iterator.Next()
			text, ok := key.(types.String)
			if !ok {
				return nil, fmt.Errorf("CEL map key must be a string")
			}
			if err := budget.visit(ctx, depth+1); err != nil {
				return nil, err
			}
			if err := budget.scalar(string(text)); err != nil {
				return nil, err
			}
			converted, err := convertCEL(ctx, value.Get(key), depth+1, budget)
			if err != nil {
				return nil, err
			}
			result[string(text)] = converted
		}
		return result, nil
	case traits.Lister:
		size, ok := value.Size().(types.Int)
		if !ok || size < 0 || int64(size) > int64(budget.maxNodes-budget.nodes) {
			return nil, fmt.Errorf("CEL list exceeds node limit")
		}
		encodedSize := 2
		if size > 0 {
			encodedSize += int(size) - 1
		}
		if err := budget.addBytes(encodedSize); err != nil {
			return nil, err
		}
		result := make([]any, int(size))
		for index := range result {
			converted, err := convertCEL(ctx, value.Get(types.Int(index)), depth+1, budget)
			if err != nil {
				return nil, err
			}
			result[index] = converted
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported CEL result type %T", value)
	}
	if err := budget.scalar(scalar); err != nil {
		return nil, err
	}
	return scalar, nil
}
