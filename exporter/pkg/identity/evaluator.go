// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package identity extracts configured submitter metadata without cluster reads.
package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/cel-go/cel"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type Evaluator struct {
	rules []compiledRule
}

type Config struct {
	SubmittedByExpression string `json:"submittedByExpression,omitempty"`
	Rules                 []Rule `json:"rules,omitempty"`
}

type Rule struct {
	Name                     string `json:"name"`
	MatchExpression          string `json:"matchExpression"`
	SubmittedByExpression    string `json:"submittedByExpression"`
	IdentitySourceExpression string `json:"identitySourceExpression"`
	SubmitterTypeExpression  string `json:"submitterTypeExpression"`
	Partial                  bool   `json:"partial"`
}

type compiledRule struct {
	name     string
	partial  bool
	programs [4]cel.Program
}

// Result distinguishes a complete identifier from a shortened or missing one.
// Complete describes the supplied identifier, not proof of its authenticity.
type Result struct {
	Submitter string
	Source    string
	Type      string
	Rule      string
	Status    string
}

const (
	Unknown            = "unknown"
	Complete           = "complete"
	Partial            = "partial"
	Conflict           = "conflict"
	EvaluationError    = "evaluation_error"
	maxRules           = 32
	maxExpressionBytes = 8192
	maxOutputBytes     = 512
	costLimit          = 10000
)

// Parse accepts the chart's JSON projection and rejects misspelled fields.
func Parse(data []byte) (*Evaluator, error) {
	if len(data) > 256*1024 {
		return nil, fmt.Errorf("identity configuration exceeds 256 KiB")
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode identity configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("identity configuration must contain one JSON object")
	}
	return New(cfg)
}

func New(cfg Config) (*Evaluator, error) {
	if cfg.SubmittedByExpression != "" {
		if len(cfg.Rules) != 0 {
			return nil, fmt.Errorf("configure submittedByExpression or rules, not both")
		}
		cfg.Rules = []Rule{{Name: "submitted-by", MatchExpression: "true", SubmittedByExpression: cfg.SubmittedByExpression, IdentitySourceExpression: "'configured-metadata'", SubmitterTypeExpression: "'unknown'", Partial: true}}
	}
	if len(cfg.Rules) == 0 || len(cfg.Rules) > maxRules {
		return nil, fmt.Errorf("identity configuration requires 1 to %d rules", maxRules)
	}
	env, err := cel.NewEnv(
		cel.Variable("labels", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("annotations", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("resource", cel.MapType(cel.StringType, cel.StringType)),
	)
	if err != nil {
		return nil, fmt.Errorf("create identity CEL environment: %w", err)
	}
	e := &Evaluator{}
	seen := map[string]bool{}
	for _, rule := range cfg.Rules {
		if rule.Name == "" || len(rule.Name) > 63 || !validOutput(rule.Name) || seen[rule.Name] {
			return nil, fmt.Errorf("identity rule names must be unique, nonempty and at most 63 bytes")
		}
		seen[rule.Name] = true
		compiled := compiledRule{name: rule.Name, partial: rule.Partial}
		for i, expr := range []string{rule.MatchExpression, rule.SubmittedByExpression, rule.IdentitySourceExpression, rule.SubmitterTypeExpression} {
			if expr == "" || len(expr) > maxExpressionBytes {
				return nil, fmt.Errorf("rule %s expression %d must contain 1 to %d bytes", rule.Name, i, maxExpressionBytes)
			}
			ast, issues := env.Compile(expr)
			if issues != nil && issues.Err() != nil {
				return nil, fmt.Errorf("compile rule %s expression %d: %w", rule.Name, i, issues.Err())
			}
			want := cel.StringType
			if i == 0 {
				want = cel.BoolType
			}
			if !ast.OutputType().IsExactType(want) {
				return nil, fmt.Errorf("rule %s expression %d must return %s", rule.Name, i, want)
			}
			program, err := env.Program(ast, cel.CostLimit(costLimit), cel.InterruptCheckFrequency(100))
			if err != nil {
				return nil, fmt.Errorf("create rule %s program: %w", rule.Name, err)
			}
			compiled.programs[i] = program
		}
		e.rules = append(e.rules, compiled)
	}
	return e, nil
}

func (e *Evaluator) Evaluate(ctx context.Context, obj metav1.Object, gvk schema.GroupVersionKind) Result {
	labels, annotations := obj.GetLabels(), obj.GetAnnotations()
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	vars := map[string]any{"labels": labels, "annotations": annotations, "resource": map[string]string{"namespace": obj.GetNamespace(), "name": obj.GetName(), "group": gvk.Group, "version": gvk.Version, "kind": gvk.Kind}}
	result := Result{Status: Unknown, Type: Unknown}
	for _, rule := range e.rules {
		matched, _, err := rule.programs[0].ContextEval(ctx, vars)
		if err != nil {
			return Result{Status: EvaluationError, Type: Unknown, Rule: rule.name}
		}
		if matched.Value() != true {
			continue
		}
		var values [3]string
		for i, program := range rule.programs[1:] {
			value, _, err := program.ContextEval(ctx, vars)
			if err != nil {
				return Result{Status: EvaluationError, Type: Unknown, Rule: rule.name}
			}
			str, ok := value.Value().(string)
			if !ok || !validOutput(str) {
				return Result{Status: EvaluationError, Type: Unknown, Rule: rule.name}
			}
			values[i] = str
			// An empty submitter is a miss, not a source/type evaluation failure.
			if i == 0 && str == "" {
				break
			}
		}
		if values[0] == "" {
			continue
		}
		if values[2] != "human" && values[2] != "service" && values[2] != Unknown {
			return Result{Status: EvaluationError, Type: Unknown, Rule: rule.name}
		}
		candidate := Result{Submitter: values[0], Source: values[1], Type: values[2], Rule: rule.name, Status: Complete}
		if rule.partial || candidate.Source == "" {
			candidate.Status = Partial
		}
		result = Merge(result, candidate)
	}
	return result
}

// Merge compares full principal keys. It never chooses a winner for a conflict.
func Merge(a, b Result) Result {
	if a.Status == EvaluationError || b.Status == EvaluationError {
		return Result{Status: EvaluationError, Type: Unknown}
	}
	if a.Status == Conflict || b.Status == Conflict {
		return Result{Status: Conflict, Type: Unknown}
	}
	if a.Submitter == "" {
		return b
	}
	if b.Submitter == "" {
		return a
	}
	if a.Submitter != b.Submitter || a.Source != b.Source || (a.Type != Unknown && b.Type != Unknown && a.Type != b.Type) {
		return Result{Status: Conflict, Type: Unknown}
	}
	if b.Status == Partial {
		a.Status = Partial
	}
	if a.Type == Unknown {
		a.Type = b.Type
	}
	if a.Rule != b.Rule {
		a.Rule = "multiple"
	}
	return a
}

func validOutput(s string) bool {
	return len(s) <= maxOutputBytes && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) == -1 && strings.TrimSpace(s) == s
}
