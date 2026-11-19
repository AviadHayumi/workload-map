// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package cel evaluates bounded expressions in explicit Kubernetes object scopes.
package cel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	celgo "github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
)

// Scope controls the identifiers available to the CEL compiler.
type Scope uint8

const (
	ReadScope Scope = iota
	WriteScope
	ObjectScope
	TargetScope
)
const MaxCost uint64 = 1_000_000

var (
	ErrCompile    = errors.New("invalid CEL expression for this scope")
	ErrEvaluation = errors.New("CEL evaluation failed")
	ErrCostLimit  = errors.New("CEL evaluation cost limit exceeded")
	ErrAbsent     = errors.New("CEL optional value is absent")
	environments  sync.Map
	programMu     sync.Mutex
	programs      = map[programKey]celgo.Program{}
)

type configuration struct {
	cost     uint64
	timeout  time.Duration
	provider func(context.Context) (map[string]any, error)
}

// Option configures expression limits or a runner's reference provider.
type Option func(*configuration)

func WithCostLimit(cost uint64) Option         { return func(c *configuration) { c.cost = cost } }
func WithTimeout(timeout time.Duration) Option { return func(c *configuration) { c.timeout = timeout } }
func WithReferenceProvider(provider func(context.Context) (map[string]any, error)) Option {
	return func(c *configuration) { c.provider = provider }
}
func settings(opts []Option) configuration {
	c := configuration{cost: MaxCost, timeout: time.Second}
	for _, o := range opts {
		o(&c)
	}
	return c
}

type programKey struct {
	scope      Scope
	expression string
	cost       uint64
}

func environment(scope Scope) (*celgo.Env, error) {
	if cached, ok := environments.Load(scope); ok {
		return cached.(*celgo.Env), nil
	}
	opts := []celgo.EnvOption{celgo.Variable("object", celgo.MapType(celgo.StringType, celgo.DynType)), celgo.OptionalTypes(), ext.Lists(), ext.Strings()}
	switch scope {
	case ReadScope, WriteScope:
		opts = append(opts, celgo.Variable("variables", celgo.MapType(celgo.StringType, celgo.DynType)), celgo.Variable("references", celgo.MapType(celgo.StringType, celgo.DynType)))
		if scope == WriteScope {
			opts = append(opts, celgo.Variable("instance", celgo.StringType), celgo.Variable("index", celgo.IntType))
		}
	case ObjectScope:
	case TargetScope:
		opts = append(opts, celgo.Variable("target", celgo.MapType(celgo.StringType, celgo.DynType)))
	default:
		return nil, errors.New("unknown CEL scope")
	}
	env, err := celgo.NewEnv(opts...)
	if err != nil {
		return nil, err
	}
	actual, _ := environments.LoadOrStore(scope, env)
	return actual.(*celgo.Env), nil
}
func program(scope Scope, source string, cost uint64) (celgo.Program, error) {
	key := programKey{scope, source, cost}
	programMu.Lock()
	cached, ok := programs[key]
	programMu.Unlock()
	if ok {
		return cached, nil
	}
	env, err := environment(scope)
	if err != nil {
		return nil, err
	}
	ast, issues := env.Compile(source)
	if issues != nil && issues.Err() != nil {
		return nil, ErrCompile
	}
	p, err := env.Program(ast, celgo.CostLimit(cost), celgo.InterruptCheckFrequency(100))
	if err != nil {
		return nil, ErrCompile
	}
	programMu.Lock()
	defer programMu.Unlock()
	if cached, ok := programs[key]; ok {
		return cached, nil
	}
	// Bound retention when applications observe many definition revisions.
	if len(programs) >= 1024 {
		clear(programs)
	}
	programs[key] = p
	return p, nil
}

// Validate compiles a workload read expression.
func Validate(source string) error { return ValidateScope(ReadScope, source) }
func ValidateScope(scope Scope, source string) error {
	_, err := program(scope, source, MaxCost)
	return err
}

// ValidateBool also rejects statically known non-boolean results.
func ValidateBool(scope Scope, source string) error {
	env, err := environment(scope)
	if err != nil {
		return err
	}
	ast, issues := env.Compile(source)
	if issues != nil && issues.Err() != nil {
		return ErrCompile
	}
	t := ast.OutputType()
	if t != celgo.BoolType && t != celgo.DynType {
		return errors.New("CEL predicate must return a boolean")
	}
	return nil
}

// Evaluate returns plain JSON-compatible values. Errors never include object data.
func Evaluate(ctx context.Context, scope Scope, source string, bindings map[string]any, opts ...Option) (any, error) {
	c := settings(opts)
	p, err := program(scope, source, c.cost)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	out, details, err := p.ContextEval(ctx, bindings)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if details != nil && details.ActualCost() != nil && *details.ActualCost() > c.cost {
			return nil, ErrCostLimit
		}
		return nil, ErrEvaluation
	}
	if types.IsError(out) || types.IsUnknown(out) {
		return nil, ErrEvaluation
	}
	return native(out)
}
func EvaluateBool(ctx context.Context, scope Scope, source string, bindings map[string]any, opts ...Option) (bool, error) {
	v, err := Evaluate(ctx, scope, source, bindings, opts...)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, errors.New("CEL predicate must return a boolean")
	}
	return b, nil
}
func native(v ref.Val) (any, error) {
	switch value := v.(type) {
	case *types.Optional:
		if !value.HasValue() {
			return nil, ErrAbsent
		}
		return native(value.GetValue())
	case types.Null:
		return nil, nil
	case types.Bool:
		return bool(value), nil
	case types.String:
		return string(value), nil
	case types.Int:
		return int64(value), nil
	case types.Uint:
		if uint64(value) > uint64(1<<63-1) {
			return nil, errors.New("CEL integer exceeds JSON signed integer range")
		}
		return int64(value), nil
	case types.Double:
		return float64(value), nil
	case traits.Lister:
		list := make([]any, 0)
		it := value.Iterator()
		for it.HasNext() == types.True {
			item, err := native(it.Next())
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
		return list, nil
	case traits.Mapper:
		result := map[string]any{}
		it := value.Iterator()
		for it.HasNext() == types.True {
			key := it.Next()
			name, ok := key.(types.String)
			if !ok {
				return nil, errors.New("CEL map keys must be strings")
			}
			item, err := native(value.Get(key))
			if err != nil {
				return nil, err
			}
			result[string(name)] = item
		}
		return result, nil
	default:
		return nil, fmt.Errorf("CEL result has unsupported type %s", v.Type().TypeName())
	}
}
