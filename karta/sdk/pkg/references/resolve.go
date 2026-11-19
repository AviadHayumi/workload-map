// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package references

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
)

type resolutionError struct {
	name, operation string
	cause           error
}

func (e resolutionError) Error() string {
	message := fmt.Sprintf("reference %q %s failed", e.name, e.operation)
	// Do not expose raw server messages, which can contain workload fields.
	switch {
	case apierrors.IsForbidden(e.cause):
		return message + ": forbidden"
	case apierrors.IsUnauthorized(e.cause):
		return message + ": unauthorized"
	default:
		return message
	}
}
func (e resolutionError) Unwrap() error { return e.cause }

// Query describes one lookup or list. The reader removes Namespace for cluster-scoped kinds.
type Query struct {
	Active          bool
	GVK             schema.GroupVersionKind
	Namespace, Name string
	Selector        labels.Selector
}

func (q Query) Equal(other Query) bool {
	if q.Active != other.Active {
		return false
	}
	if !q.Active {
		return true
	}
	if q.GVK != other.GVK || q.Namespace != other.Namespace || q.Name != other.Name {
		return false
	}
	if q.Selector == nil || other.Selector == nil {
		return q.Selector == nil && other.Selector == nil
	}
	return q.Selector.String() == other.Selector.String()
}

// Plan evaluates only this reference's eligibility and selector, never a client call.
func Plan(ctx context.Context, definition api.Reference, workload any, opts ...cel.Option) (Query, error) {
	if err := ctx.Err(); err != nil {
		return Query{}, err
	}
	object, err := cel.CopyJSON(workload)
	if err != nil {
		return Query{}, err
	}
	bindings := map[string]any{"object": object}
	if definition.MatchExpression != "" {
		active, err := cel.EvaluateBool(ctx, cel.ObjectScope, definition.MatchExpression, bindings, opts...)
		if err != nil {
			return Query{}, fmt.Errorf("reference %q eligibility: %w", definition.Name, err)
		}
		if !active {
			return Query{}, nil
		}
	}
	gv, err := schema.ParseGroupVersion(definition.APIVersion)
	if err != nil || definition.APIVersion == "" || definition.Kind == "" {
		return Query{}, errors.New("invalid reference apiVersion or kind")
	}
	if (definition.Lookup == nil) == (definition.List == nil) {
		return Query{}, errors.New("reference needs exactly one lookup or list")
	}
	q := Query{Active: true, GVK: gv.WithKind(definition.Kind)}
	if obj, ok := object.(map[string]any); ok {
		if metadata, ok := obj["metadata"].(map[string]any); ok {
			q.Namespace, _ = metadata["namespace"].(string)
		}
	}
	text := func(source string) (string, error) {
		value, err := cel.Evaluate(ctx, cel.ObjectScope, source, bindings, opts...)
		if err != nil {
			return "", err
		}
		s, ok := value.(string)
		if !ok {
			return "", errors.New("reference expression must return a string")
		}
		return s, nil
	}
	if definition.Lookup != nil {
		q.Name, err = text(definition.Lookup.NameExpression)
		if err != nil {
			return Query{}, fmt.Errorf("reference %q lookup: %w", definition.Name, err)
		}
		if q.Name == "" {
			return Query{}, fmt.Errorf("reference %q lookup returned an empty name", definition.Name)
		}
		return q, nil
	}
	resolveValue := func(value api.LabelValue) (string, error) {
		if (value.Value == nil) == (value.Expression == nil) {
			return "", errors.New("label requires exactly one value or expression")
		}
		if value.Value != nil {
			return *value.Value, nil
		}
		return text(*value.Expression)
	}
	selector := labels.NewSelector()
	add := func(key string, op selection.Operator, values []api.LabelValue) error {
		resolved := make([]string, 0, len(values))
		for _, v := range values {
			s, err := resolveValue(v)
			if err != nil {
				return err
			}
			resolved = append(resolved, s)
		}
		requirement, err := labels.NewRequirement(key, op, resolved)
		if err != nil {
			return errors.New("invalid reference label selector")
		}
		selector = selector.Add(*requirement)
		return nil
	}
	for key, value := range definition.List.MatchLabels {
		if err := add(key, selection.Equals, []api.LabelValue{value}); err != nil {
			return Query{}, fmt.Errorf("reference %q selector: %w", definition.Name, err)
		}
	}
	for _, r := range definition.List.MatchExpressions {
		operators := map[api.LabelSelectorOperator]selection.Operator{api.LabelSelectorOpIn: selection.In, api.LabelSelectorOpNotIn: selection.NotIn, api.LabelSelectorOpExists: selection.Exists, api.LabelSelectorOpDoesNotExist: selection.DoesNotExist}
		op, ok := operators[r.Operator]
		if !ok {
			return Query{}, errors.New("invalid reference selector operator")
		}
		if err := add(r.Key, op, r.Values); err != nil {
			return Query{}, fmt.Errorf("reference %q selector: %w", definition.Name, err)
		}
	}
	if selector.Empty() {
		return Query{}, errors.New("reference list requires a label selector")
	}
	q.Selector = selector
	return q, nil
}

// ResolveDefinitions resolves only the supplied declarations. Failed calls are not cached.
func ResolveDefinitions(ctx context.Context, reader ResourceReader, definitions []api.Reference, workload any, opts ...cel.Option) (ResolvedReferences, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := ResolvedReferences{}
	for _, definition := range definitions {
		q, err := Plan(ctx, definition, workload, opts...)
		if err != nil {
			return nil, err
		}
		if !q.Active {
			continue
		}
		if reader == nil {
			return nil, errors.New("reference resolution requires a reader")
		}
		if q.Selector == nil {
			object, err := reader.Get(ctx, q.GVK, q.Namespace, q.Name)
			if errors.Is(err, ErrNotFound) || apierrors.IsNotFound(err) {
				result[definition.Name] = NewLookupValue(nil)
				continue
			}
			if err != nil {
				return nil, resolutionError{definition.Name, "lookup", err}
			}
			result[definition.Name] = NewLookupValue(object)
		} else {
			objects, err := reader.List(ctx, q.GVK, ListQuery{Namespace: q.Namespace, Selector: q.Selector})
			if err != nil {
				return nil, resolutionError{definition.Name, "list", err}
			}
			result[definition.Name] = NewListValue(objects)
		}
	}
	return result, nil
}
func Resolve(ctx context.Context, reader ResourceReader, karta *api.Karta, workload any, opts ...cel.Option) (ResolvedReferences, error) {
	if karta == nil {
		return nil, errors.New("karta is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	programs, err := api.ProgramsFor(karta)
	if err != nil {
		return nil, err
	}
	return ResolveDefinitions(ctx, reader, karta.Spec.StructureDefinition.References, workload, append([]cel.Option{cel.WithPrograms(programs)}, opts...)...)
}

// ResolveSupplied applies eligibility before exposing a copied supplied snapshot.
func ResolveSupplied(ctx context.Context, definitions []api.Reference, workload any, supplied ResolvedReferences, opts ...cel.Option) (ResolvedReferences, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	declared := map[string]bool{}
	for _, definition := range definitions {
		declared[definition.Name] = true
	}
	for name := range supplied {
		if !declared[name] {
			return nil, fmt.Errorf("unknown supplied reference %q", name)
		}
	}
	result := ResolvedReferences{}
	for _, definition := range definitions {
		q, err := Plan(ctx, definition, workload, opts...)
		if err != nil {
			return nil, err
		}
		if !q.Active {
			continue
		}
		value, exists := supplied[definition.Name]
		if !exists {
			continue
		}
		if err := validateSuppliedValue(definition, value); err != nil {
			return nil, err
		}
		objects := value.List
		if value.Object != nil {
			if value.Object.GetName() != q.Name {
				return nil, fmt.Errorf("reference %q has an unexpected lookup name", definition.Name)
			}
			objects = append(objects, *value.Object)
		}
		for _, object := range objects {
			if object.GetNamespace() != "" && object.GetNamespace() != q.Namespace {
				return nil, fmt.Errorf("reference %q has an unexpected namespace", definition.Name)
			}
		}
		result[definition.Name] = value
	}
	return result.Copy()
}
