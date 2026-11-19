// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	celpkg "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
)

// InstanceNotFoundError is returned when a pod's extracted instance ID doesn't match any valid instance IDs
type InstanceNotFoundError string

func (e InstanceNotFoundError) Error() string {
	return string(e)
}

// PodQuerier evaluates selector expressions against a pod, bound as `object`.
type PodQuerier struct {
	pod     *corev1.Pod
	options []celpkg.Option

	celOnce      sync.Once
	celEvaluator expression.Runner
	celErr       error
}

// celOnPod returns a CEL runner bound to the pod, built once on first use.
func (pq *PodQuerier) celOnPod() (expression.Runner, error) {
	pq.celOnce.Do(func() {
		pq.celEvaluator, pq.celErr = celpkg.NewPodRunner(pq.pod, pq.options...)
	})

	return pq.celEvaluator, pq.celErr
}

// evaluateSelectorField reads one selector expression as a string.
func (pq *PodQuerier) evaluateSelectorField(ctx context.Context, expr string) (string, error) {
	evaluator, err := pq.celOnPod()
	if err != nil {
		return "", err
	}

	return evaluateStringWith(ctx, evaluator, expr)
}

func NewPodQuerier(pod *corev1.Pod, opts ...celpkg.Option) *PodQuerier {
	return &PodQuerier{pod: pod, options: opts}
}

// NewPodQuerier evaluates Pod expressions with the workload definition's programs and limits.
func (f *ComponentFactory) NewPodQuerier(pod *corev1.Pod) *PodQuerier {
	if f.settings == nil {
		return NewPodQuerier(pod)
	}
	return NewPodQuerier(pod, f.settings.evaluation...)
}

func (pq *PodQuerier) GetPodName() string {
	return pq.pod.Name
}

// MatchesComponentType returns true if the pod matches the given component type selector
func (pq *PodQuerier) MatchesComponentType(ctx context.Context, selector *v1alpha1.ComponentTypeSelector) (bool, error) {
	if selector == nil {
		return false, nil
	}

	evaluator, err := pq.celOnPod()
	if err != nil {
		return false, err
	}
	matched, err := pq.matchesRead(ctx, selector.Read)
	if err != nil || !matched {
		return false, err
	}
	key := selector.Read.Expression
	if selector.Value == nil {
		// Existence check: key should exist and not be nil
		return checkKeyExistsWith(ctx, evaluator, key)
	}

	// Equality check: key should equal the specified value
	return checkKeyValueWith(ctx, evaluator, key, *selector.Value)
}

// checkKeyExists returns true if the key exists
func checkKeyExistsWith(ctx context.Context, evaluator expression.Evaluator, keyPath string) (bool, error) {
	results, err := evaluator.Evaluate(ctx, keyPath)
	if err != nil {
		return false, err
	}

	// Key exists if we get any non-nil result
	for _, result := range results {
		if result != nil {
			return true, nil
		}
	}
	return false, nil
}

// checkKeyValue returns true if the key equals the expected value. The field is evaluated alone
// and compared here: building `<field> == "v"` as source breaks under the CEL checker, which
// types an optional chain ending in orValue(null) as null and rejects the comparison.
func checkKeyValueWith(ctx context.Context, evaluator expression.Evaluator, keyPath, expectedValue string) (bool, error) {
	results, err := evaluator.Evaluate(ctx, keyPath)
	if err != nil {
		return false, err
	}

	for _, result := range results {
		if value, ok := result.(string); ok && value == expectedValue {
			return true, nil
		}
	}

	return false, nil
}

// ExtractInstanceId extracts the component instance identifier from the pod using the given ComponentInstanceSelector.
// Returns the instance id as a string, a boolean indicating whether a value was found, and an error.
// When the selector is nil or has an empty expression, found is false with no error.
func (pq *PodQuerier) ExtractInstanceId(ctx context.Context, instanceSelector *v1alpha1.ComponentInstanceSelector) (string, bool, error) {
	if instanceSelector == nil || instanceSelector.Read.Expression == "" {
		return "", false, nil
	}

	matched, err := pq.matchesRead(ctx, instanceSelector.Read)
	if err != nil || !matched {
		return "", false, err
	}
	value, err := pq.evaluateSelectorField(ctx, instanceSelector.Read.Expression)
	if err != nil {
		return "", false, fmt.Errorf("failed to extract instance id from %q: %w", instanceSelector.Read.Expression, err)
	}
	return value, true, nil
}

// ExtractReplicaKey extracts the replica identifier from the pod using the given ReplicaSelector.
// Returns the replica key as a string, a boolean indicating whether a value was found, and an error.
// When the selector is nil or has an empty expression, found is false with no error.
func (pq *PodQuerier) ExtractReplicaKey(ctx context.Context, selector *v1alpha1.ReplicaSelector) (string, bool, error) {
	if selector == nil || selector.Read.Expression == "" {
		return "", false, nil
	}

	matched, err := pq.matchesRead(ctx, selector.Read)
	if err != nil || !matched {
		return "", false, err
	}
	value, err := pq.evaluateSelectorField(ctx, selector.Read.Expression)
	if err != nil {
		return "", false, fmt.Errorf("failed to extract replica key from %q: %w", selector.Read.Expression, err)
	}
	return value, true, nil
}

// ExtractGroupKeysFor extracts the member's grouping key values from the pod.
func (pq *PodQuerier) ExtractGroupKeysFor(ctx context.Context, member v1alpha1.PodGroupMemberDefinition) ([]string, error) {
	groupKeys := make([]string, 0, len(member.GroupByExpressions))
	for _, expr := range member.GroupByExpressions {
		value, err := pq.evaluateSelectorField(ctx, expr)
		if err != nil {
			return nil, fmt.Errorf("failed to extract group key from expression %q: %w", expr, err)
		}
		groupKeys = append(groupKeys, value)
	}

	return groupKeys, nil
}

// GetMatchingInstanceId checks if the pod matches any of the provided instance ids using the instance selector.
// Returns the matching instance id if found, empty string if no match.
func (pq *PodQuerier) GetMatchingInstanceId(ctx context.Context, instanceSelector *v1alpha1.ComponentInstanceSelector, instanceIds []string) (string, error) {
	if instanceSelector == nil {
		// No instance selector - check if single instance with empty id is expected
		if len(instanceIds) == 1 && instanceIds[0] == "" {
			return "", nil // Match for single instance with empty id
		}
		return "", fmt.Errorf("no instance selector provided but instance ids are not empty")
	}

	podInstanceId, _, err := pq.ExtractInstanceId(ctx, instanceSelector)
	if err != nil {
		return "", err
	}

	// Check if pod's instance id matches any of the existing instance ids
	for _, id := range instanceIds {
		if podInstanceId == id {
			return id, nil
		}
	}

	return "", InstanceNotFoundError(fmt.Sprintf("could not match instance id %q. existing instance ids %v", podInstanceId, instanceIds))
}

// evaluateStringWith evaluates one field with the given engine and stringifies the single result.
func evaluateStringWith(ctx context.Context, evaluator expression.Evaluator, expr string) (string, error) {
	results, err := evaluator.Evaluate(ctx, expr)
	if err != nil {
		return "", err
	}
	if err := validateSingleQueryResult(results); err != nil {
		return "", err
	}

	return fmt.Sprintf("%v", results[0]), nil
}

func validateSingleQueryResult(results []any) error {
	if len(results) != 1 {
		return fmt.Errorf("expected single query result, got %d", len(results))
	}
	if results[0] == nil || results[0] == "" {
		return fmt.Errorf("query result is empty %v", results[0])
	}
	return nil
}

func (pq *PodQuerier) matchesRead(ctx context.Context, read v1alpha1.ReadAccessor) (bool, error) {
	if read.MatchExpression == "" {
		return true, nil
	}
	evaluator, err := pq.celOnPod()
	if err != nil {
		return false, err
	}
	results, err := evaluator.EvaluateWithVariables(ctx, read.MatchExpression, nil)
	if err != nil {
		return false, err
	}
	return booleanResult(results)
}

// PassesFilters requires every Pod expression to return true.
func (pq *PodQuerier) PassesFilters(ctx context.Context, filters []string) (bool, error) {
	evaluator, err := pq.celOnPod()
	if err != nil {
		return false, err
	}
	for _, filter := range filters {
		results, err := evaluator.EvaluateWithVariables(ctx, filter, nil)
		if err != nil {
			return false, fmt.Errorf("evaluate Pod filter: %w", err)
		}
		matched, err := booleanResult(results)
		if err != nil {
			return false, err
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
}
