// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cel

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
)

// Dependencies describes free uses of the two named bindings.
type Dependencies struct {
	Variables, References []string
	UsesReferences        bool
	AllVariables          bool
}

// Analyze uses the CEL AST, so strings, object fields and local iteration names
// do not create dependencies. Dynamic variable selection conservatively uses all variables.
func Analyze(source string, opts ...Option) (Dependencies, error) {
	p := settings(opts).programs
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.sourceLocked(source)
	if err != nil {
		return Dependencies{}, err
	}
	d := s.deps
	d.Variables = slices.Clone(d.Variables)
	d.References = slices.Clone(d.References)
	return d, nil
}

func dependencies(root celast.Expr) Dependencies {
	var result Dependencies
	variables, refs := map[string]bool{}, map[string]bool{}
	var walk func(celast.Expr, map[string]bool)
	walk = func(e celast.Expr, bound map[string]bool) {
		if e == nil {
			return
		}
		root := func(e celast.Expr, name string) bool {
			return e.Kind() == celast.IdentKind && e.AsIdent() == name && !bound[name]
		}
		add := func(name, key string) {
			if name == "variables" {
				variables[key] = true
			} else {
				refs[key] = true
				result.UsesReferences = true
			}
		}
		switch e.Kind() {
		case celast.IdentKind:
			if !bound[e.AsIdent()] {
				if e.AsIdent() == "references" {
					result.UsesReferences = true
				}
				if e.AsIdent() == "variables" {
					result.AllVariables = true
				}
			}
		case celast.SelectKind:
			s := e.AsSelect()
			for _, name := range []string{"variables", "references"} {
				if root(s.Operand(), name) {
					add(name, s.FieldName())
					return
				}
			}
			walk(s.Operand(), bound)
		case celast.CallKind:
			c := e.AsCall()
			args := c.Args()
			if (c.FunctionName() == "_[_]" || c.FunctionName() == "_[?_]" || c.FunctionName() == "_?._") && len(args) == 2 {
				for _, name := range []string{"variables", "references"} {
					if root(args[0], name) && args[1].Kind() == celast.LiteralKind {
						if key, ok := args[1].AsLiteral().(types.String); ok {
							add(name, string(key))
							return
						}
					}
				}
			}
			if c.IsMemberFunction() {
				walk(c.Target(), bound)
			}
			for _, a := range args {
				walk(a, bound)
			}
		case celast.ListKind:
			for _, a := range e.AsList().Elements() {
				walk(a, bound)
			}
		case celast.MapKind:
			for _, a := range e.AsMap().Entries() {
				walk(a.AsMapEntry().Key(), bound)
				walk(a.AsMapEntry().Value(), bound)
			}
		case celast.StructKind:
			for _, a := range e.AsStruct().Fields() {
				walk(a.AsStructField().Value(), bound)
			}
		case celast.ComprehensionKind:
			c := e.AsComprehension()
			walk(c.IterRange(), bound)
			walk(c.AccuInit(), bound)
			nested := maps.Clone(bound)
			nested[c.AccuVar()] = true
			nested[c.IterVar()] = true
			nested[c.IterVar2()] = true
			walk(c.LoopCondition(), nested)
			walk(c.LoopStep(), nested)
			resultScope := maps.Clone(bound)
			resultScope[c.AccuVar()] = true
			walk(c.Result(), resultScope)
		}
	}
	walk(root, map[string]bool{})
	result.Variables = slices.Sorted(maps.Keys(variables))
	result.References = slices.Sorted(maps.Keys(refs))
	return result
}

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ValidateVariables checks unique names, expression scopes and dependency cycles.
func ValidateVariables(variables []expression.NamedExpression, opts ...Option) error {
	names := map[string]string{}
	for _, v := range variables {
		if !identifier.MatchString(v.Name) {
			return errors.New("invalid variable name")
		}
		if _, ok := names[v.Name]; ok {
			return fmt.Errorf("duplicate variable %q", v.Name)
		}
		names[v.Name] = v.Expression
		if err := ValidateScope(ReadScope, v.Expression, opts...); err != nil {
			return fmt.Errorf("variable %q: %w", v.Name, err)
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("variable dependency cycle at %q", name)
		}
		if visited[name] {
			return nil
		}
		source, ok := names[name]
		if !ok {
			return fmt.Errorf("unknown variable %q", name)
		}
		visiting[name] = true
		deps, err := Analyze(source, opts...)
		if err != nil {
			return err
		}
		if deps.AllVariables {
			return fmt.Errorf("variable %q must use static variable names", name)
		}
		for _, dep := range deps.Variables {
			if err = visit(dep); err != nil {
				return err
			}
		}
		delete(visiting, name)
		visited[name] = true
		return nil
	}
	for _, v := range variables {
		if err := visit(v.Name); err != nil {
			return err
		}
	}
	return nil
}
