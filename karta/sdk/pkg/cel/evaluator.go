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
	standalone    = &Programs{limit: 1024}
)

type configuration struct {
	programs *Programs
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
	c := configuration{programs: standalone, cost: MaxCost, timeout: time.Second}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// Programs retains checked expressions for one definition revision. A zero value
// is ready to use. Programs never stores workload objects or reference values.
type Programs struct {
	mu      sync.Mutex
	sources map[string]*parsedSource
	limit   int // only standalone expressions lack a definition to bound their lifetime
}
type parsedSource struct {
	parsed  *celgo.Ast
	deps    Dependencies
	checked map[Scope]*checkedExpression
}
type checkedExpression struct {
	env       *celgo.Env
	ast       *celgo.Ast
	programMu sync.Mutex
	// Each caller-selected budget has its own interpreter, built from the same AST.
	executables map[uint64]celgo.Program
}

func WithPrograms(p *Programs) Option {
	return func(c *configuration) {
		if p != nil {
			c.programs = p
		}
	}
}

// WithLimits overrides each nonzero limit, leaving the other setting at its default.
func WithLimits(cost uint64, timeout time.Duration) Option {
	return func(c *configuration) {
		if cost > 0 {
			c.cost = cost
		}
		if timeout > 0 {
			c.timeout = timeout
		}
	}
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

// sourceLocked parses one source for all scopes. The parser environment has
// the same extensions and macros as the environments used for type checking.
func (p *Programs) sourceLocked(source string) (*parsedSource, error) {
	if s := p.sources[source]; s != nil {
		return s, nil
	}
	env, err := environment(ReadScope)
	if err != nil {
		return nil, err
	}
	ast, issues := env.Parse(source)
	if issues != nil && issues.Err() != nil {
		return nil, ErrCompile
	}
	s := &parsedSource{parsed: ast, deps: dependencies(ast.NativeRep().Expr()), checked: make(map[Scope]*checkedExpression)}
	if p.sources == nil {
		p.sources = make(map[string]*parsedSource)
	}
	if p.limit > 0 && len(p.sources) >= p.limit {
		clear(p.sources)
	}
	p.sources[source] = s
	return s, nil
}

func (p *Programs) check(scope Scope, source string) (*checkedExpression, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.sourceLocked(source)
	if err != nil {
		return nil, err
	}
	if e := s.checked[scope]; e != nil {
		return e, nil
	}
	env, err := environment(scope)
	if err != nil {
		return nil, err
	}
	ast, issues := env.Check(s.parsed)
	if issues != nil && issues.Err() != nil {
		return nil, ErrCompile
	}
	e := &checkedExpression{env: env, ast: ast}
	s.checked[scope] = e
	return e, nil
}
func (e *checkedExpression) program(cost uint64) (celgo.Program, error) {
	e.programMu.Lock()
	defer e.programMu.Unlock()
	if prog := e.executables[cost]; prog != nil {
		return prog, nil
	}
	prog, err := e.env.Program(e.ast, celgo.CostLimit(cost), celgo.InterruptCheckFrequency(100))
	if err != nil {
		return nil, ErrCompile
	}
	if e.executables == nil {
		e.executables = make(map[uint64]celgo.Program)
	}
	e.executables[cost] = prog
	return prog, nil
}

// Validate compiles a workload read expression.
func Validate(source string) error { return ValidateScope(ReadScope, source) }
func ValidateScope(scope Scope, source string, opts ...Option) error {
	c := settings(opts)
	e, err := c.programs.check(scope, source)
	if err != nil {
		return err
	}
	_, err = e.program(c.cost)
	return err
}

// ValidateBool also rejects statically known non-boolean results.
func ValidateBool(scope Scope, source string, opts ...Option) error {
	c := settings(opts)
	checked, err := c.programs.check(scope, source)
	if err != nil {
		return err
	}
	t := checked.ast.OutputType()
	if t != celgo.BoolType && t != celgo.DynType {
		return errors.New("CEL predicate must return a boolean")
	}
	_, err = checked.program(c.cost)
	return err
}

// Evaluate returns plain JSON-compatible values. Errors never include object data.
func Evaluate(ctx context.Context, scope Scope, source string, bindings map[string]any, opts ...Option) (any, error) {
	c := settings(opts)
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checked, err := c.programs.check(scope, source)
	if err != nil {
		return nil, err
	}
	p, err := checked.program(c.cost)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}

	out, details, err := p.ContextEval(ctx, bindings)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if details != nil && details.ActualCost() != nil && *details.ActualCost() > c.cost {
			return nil, ErrCostLimit
		}
		return nil, ErrEvaluation
	}
	if types.IsError(out) || types.IsUnknown(out) {
		return nil, ErrEvaluation
	}
	result, err := native(ctx, out)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result, err
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
func native(ctx context.Context, v ref.Val) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch value := v.(type) {
	case *types.Optional:
		if !value.HasValue() {
			return nil, ErrAbsent
		}
		return native(ctx, value.GetValue())
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
			item, err := native(ctx, it.Next())
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
			item, err := native(ctx, value.Get(key))
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
