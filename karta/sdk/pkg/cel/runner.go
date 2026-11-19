// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cel

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
)

type NamedExpression = expression.NamedExpression

type runner struct {
	object     any
	scope      Scope
	variables  []NamedExpression
	options    []Option
	provider   func(context.Context) (map[string]any, error)
	refMu      sync.Mutex
	references map[string]any
}

func NewRunner(source any, opts ...Option) (expression.Runner, error) {
	return NewRunnerWithVariables(source, nil, opts...)
}
func NewRunnerWithVariables(source any, variables []NamedExpression, opts ...Option) (expression.Runner, error) {
	if err := ValidateVariables(variables, opts...); err != nil {
		return nil, err
	}
	object, err := CopyJSON(source)
	if err != nil {
		return nil, err
	}
	return &runner{object: object, scope: ReadScope, variables: append([]NamedExpression(nil), variables...), options: opts, provider: settings(opts).provider}, nil
}

// NewPodRunner binds only object. Pod selectors cannot read workload variables or references.
func NewPodRunner(source any, opts ...Option) (expression.Runner, error) {
	r, err := NewRunner(source, opts...)
	if err != nil {
		return nil, err
	}
	r.(*runner).scope = ObjectScope
	return r, nil
}
func (r *runner) GetObject() (any, error) { return CopyJSON(r.object) }
func (r *runner) Assign(ctx context.Context, path string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path != "" && path != "object" {
		return errors.New("runner assignment only supports the document root")
	}
	copied, err := CopyJSON(value)
	if err != nil {
		return err
	}
	r.object = copied
	r.refMu.Lock()
	r.references = nil
	r.refMu.Unlock()
	return nil
}
func (r *runner) resolveReferences(ctx context.Context) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.provider == nil {
		return nil, expression.ErrReferencesNotSupported
	}
	r.refMu.Lock()
	defer r.refMu.Unlock()
	if r.references != nil {
		return r.references, nil
	}
	refs, err := r.provider(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if refs == nil {
		refs = map[string]any{}
	}
	copied, err := CopyJSON(refs)
	if err != nil {
		return nil, err
	}
	r.references = copied.(map[string]any)
	return r.references, nil
}
func (r *runner) needed(ctx context.Context, sources []string) (map[string]bool, bool, error) {
	byName := map[string]string{}
	for _, v := range r.variables {
		byName[v.Name] = v.Expression
	}
	needed := map[string]bool{}
	usesRefs := false
	var scan func(string) error
	add := func(name string) error {
		if needed[name] {
			return nil
		}
		source, ok := byName[name]
		if !ok {
			return fmt.Errorf("unknown variable %q", name)
		}
		needed[name] = true
		return scan(source)
	}
	scan = func(source string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		deps, err := Analyze(source, r.options...)
		if err != nil {
			return err
		}
		usesRefs = usesRefs || deps.UsesReferences
		if deps.AllVariables {
			for _, v := range r.variables {
				if err = add(v.Name); err != nil {
					return err
				}
			}
		} else {
			for _, name := range deps.Variables {
				if err = add(name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if len(sources) == 0 {
		for _, v := range r.variables {
			if err := add(v.Name); err != nil {
				return nil, false, err
			}
		}
	} else {
		for _, s := range sources {
			if err := scan(s); err != nil {
				return nil, false, err
			}
		}
	}
	return needed, usesRefs, nil
}
func (r *runner) variablesFor(ctx context.Context, needed map[string]bool, refs map[string]any) (map[string]any, error) {
	byName := map[string]string{}
	for _, v := range r.variables {
		byName[v.Name] = v.Expression
	}
	resolved := map[string]any{}
	var resolve func(string) error
	resolve = func(name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := resolved[name]; ok {
			return nil
		}
		deps, err := Analyze(byName[name], r.options...)
		if err != nil {
			return err
		}
		for _, dep := range deps.Variables {
			if err = resolve(dep); err != nil {
				return err
			}
		}
		value, err := Evaluate(ctx, ReadScope, byName[name], map[string]any{"object": r.object, "variables": resolved, "references": refs}, r.options...)
		if err != nil {
			return fmt.Errorf("variable %q: %w", name, err)
		}
		resolved[name] = value
		return nil
	}
	for _, v := range r.variables {
		if needed[v.Name] {
			if err := resolve(v.Name); err != nil {
				return nil, err
			}
		}
	}
	return resolved, nil
}
func (r *runner) ResolveVariables(ctx context.Context, sources ...string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	needed, usesRefs, err := r.needed(ctx, sources)
	if err != nil {
		return nil, err
	}
	refs := map[string]any{}
	if usesRefs {
		refs, err = r.resolveReferences(ctx)
		if err != nil {
			return nil, err
		}
	}
	return r.variablesFor(ctx, needed, refs)
}
func (r *runner) evaluate(ctx context.Context, scope Scope, source string, extra map[string]any) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateScope(scope, source, r.options...); err != nil {
		return nil, err
	}
	bindings := map[string]any{"object": r.object}
	if scope == ReadScope || scope == WriteScope {
		needed, usesRefs, err := r.needed(ctx, []string{source})
		if err != nil {
			return nil, err
		}
		vars, frozen := extra["variables"]
		if frozen {
			deps, err := Analyze(source, r.options...)
			if err != nil {
				return nil, err
			}
			usesRefs = deps.UsesReferences
		}
		refs := map[string]any{}
		if usesRefs {
			refs, err = r.resolveReferences(ctx)
			if err != nil {
				return nil, err
			}
		}
		if !frozen {
			vars, err = r.variablesFor(ctx, needed, refs)
			if err != nil {
				return nil, err
			}
		}
		bindings["variables"] = vars
		bindings["references"] = refs
		if scope == WriteScope {
			bindings["instance"] = ""
			bindings["index"] = int64(0)
			for _, key := range []string{"instance", "index"} {
				if extra[key] != nil {
					normalized, err := CopyJSON(extra[key])
					if err != nil {
						return nil, err
					}
					bindings[key] = normalized
				}
			}
		}
	}
	value, err := Evaluate(ctx, scope, source, bindings, r.options...)
	if err != nil {
		return nil, err
	}
	return []any{value}, nil
}
func (r *runner) EvaluateWithVariables(ctx context.Context, source string, vars map[string]any) ([]any, error) {
	return r.evaluate(ctx, r.scope, source, vars)
}
func (r *runner) EvaluateWritePath(ctx context.Context, source string, vars map[string]any) ([]any, error) {
	if r.scope == ObjectScope {
		return nil, errors.New("pod runner cannot write")
	}
	return r.evaluate(ctx, WriteScope, source, vars)
}
func (r *runner) Evaluate(ctx context.Context, source string) ([]any, error) {
	out, err := r.evaluate(ctx, r.scope, source, nil)
	if err != nil {
		return nil, err
	}
	if list, ok := out[0].([]any); ok {
		return list, nil
	}
	return out, nil
}
