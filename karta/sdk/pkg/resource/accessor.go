// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"k8s.io/utils/ptr"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
)

// DefinitionNotFoundError represents an error when a requested definition is not found
type DefinitionNotFoundError string

func (e DefinitionNotFoundError) Error() string {
	return string(e)
}

// Accessor implements extraction and updating of resource data through the engine contract.
type Accessor struct {
	runner expression.Runner
}

func NewAccessor(runner expression.Runner) *Accessor { return &Accessor{runner: runner} }

// GetObject returns the object as a map[string]interface{}
func (a *Accessor) GetObject() (map[string]interface{}, error) {
	object, err := a.runner.GetObject()
	if err != nil {
		return nil, err
	}
	objectMap, ok := object.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("object is not a map[string]interface{}")
	}
	return objectMap, nil
}

func (a *Accessor) ExtractPodTemplateSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]corev1.PodTemplateSpec, error) {
	if definition.SpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.PodTemplateSpec == nil || definition.SpecDefinition.PodTemplateSpec.Read == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod template spec definition", definition.Name))
	}

	var podTemplateSpec []corev1.PodTemplateSpec
	err := extractVia(ctx, definition.SpecDefinition.PodTemplateSpec, instancedComponent(definition), a.runner, &podTemplateSpec)

	return podTemplateSpec, err
}

func (a *Accessor) ExtractPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]corev1.PodSpec, error) {
	if definition.SpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.PodSpec == nil || definition.SpecDefinition.PodSpec.Read == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod spec definition", definition.Name))
	}

	var podSpec []corev1.PodSpec
	err := extractVia(ctx, definition.SpecDefinition.PodSpec, instancedComponent(definition), a.runner, &podSpec)

	return podSpec, err
}

func (a *Accessor) ExtractPodMetadata(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]metav1.ObjectMeta, error) {
	if definition.SpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.Metadata == nil || definition.SpecDefinition.Metadata.Read == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod metadata definition", definition.Name))
	}

	var podMetadata []metav1.ObjectMeta
	err := extractVia(ctx, definition.SpecDefinition.Metadata, instancedComponent(definition), a.runner, &podMetadata)

	return podMetadata, err
}

func (a *Accessor) ExtractScale(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]Scale, error) {
	if definition.ScaleDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have scale definition", definition.Name))
	}

	var (
		replicas    []*int32
		minReplicas []*int32
		maxReplicas []*int32
	)

	scaleCount := 0

	if err := extractOptionalVia(ctx, definition.ScaleDefinition.Replicas, instancedComponent(definition), a.runner, &replicas); err != nil {
		return nil, err
	}
	scaleCount = max(scaleCount, len(replicas))

	if err := extractOptionalVia(ctx, definition.ScaleDefinition.MinReplicas, instancedComponent(definition), a.runner, &minReplicas); err != nil {
		return nil, err
	}
	scaleCount = max(scaleCount, len(minReplicas))

	if err := extractOptionalVia(ctx, definition.ScaleDefinition.MaxReplicas, instancedComponent(definition), a.runner, &maxReplicas); err != nil {
		return nil, err
	}
	scaleCount = max(scaleCount, len(maxReplicas))

	if scaleCount == 0 {
		return nil, ErrValueNotFound
	}
	scales := make([]Scale, scaleCount)
	for i := 0; i < scaleCount; i++ {
		scales[i] = Scale{
			Replicas:    safeGetByIndex(replicas, i),
			MaxReplicas: safeGetByIndex(maxReplicas, i),
			MinReplicas: safeGetByIndex(minReplicas, i),
		}
	}

	return scales, nil
}

func (a *Accessor) ExtractFragmentedPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]FragmentedPodSpec, error) {
	if definition.SpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.FragmentedPodSpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have fragmented pod spec definition", definition.Name))
	}

	fragmentedDefinition := definition.SpecDefinition.FragmentedPodSpecDefinition

	var (
		schedulerNameResults     []string
		labelsResults            []map[string]string
		annotationsResults       []map[string]string
		resourcesResults         []*corev1.ResourceRequirements
		resourceClaimsResults    [][]corev1.PodResourceClaim
		podAffinityResults       []*corev1.PodAffinity
		nodeAffinityResults      []*corev1.NodeAffinity
		containersResults        [][]corev1.Container
		containerResults         []*corev1.Container
		priorityClassNameResults []string
		imageResults             []string
	)

	specCount := 0

	if err := extractOptionalVia(ctx, fragmentedDefinition.SchedulerName, instancedComponent(definition), a.runner, &schedulerNameResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(schedulerNameResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.Labels, instancedComponent(definition), a.runner, &labelsResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(labelsResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.Annotations, instancedComponent(definition), a.runner, &annotationsResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(annotationsResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.Resources, instancedComponent(definition), a.runner, &resourcesResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(resourcesResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.ResourceClaims, instancedComponent(definition), a.runner, &resourceClaimsResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(resourceClaimsResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.PodAffinity, instancedComponent(definition), a.runner, &podAffinityResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(podAffinityResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.NodeAffinity, instancedComponent(definition), a.runner, &nodeAffinityResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(nodeAffinityResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.Containers, instancedComponent(definition), a.runner, &containersResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(containersResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.Container, instancedComponent(definition), a.runner, &containerResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(containerResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.PriorityClassName, instancedComponent(definition), a.runner, &priorityClassNameResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(priorityClassNameResults))

	if err := extractOptionalVia(ctx, fragmentedDefinition.Image, instancedComponent(definition), a.runner, &imageResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(imageResults))

	if specCount == 0 {
		return nil, ErrValueNotFound
	}
	fragmentedSpecs := make([]FragmentedPodSpec, specCount)
	for i := 0; i < specCount; i++ {
		fragmentedSpecs[i] = FragmentedPodSpec{
			SchedulerName:     safeGetByIndex(schedulerNameResults, i),
			Labels:            safeGetByIndex(labelsResults, i),
			Annotations:       safeGetByIndex(annotationsResults, i),
			Resources:         safeGetByIndex(resourcesResults, i),
			ResourceClaims:    safeGetByIndex(resourceClaimsResults, i),
			PodAffinity:       safeGetByIndex(podAffinityResults, i),
			NodeAffinity:      safeGetByIndex(nodeAffinityResults, i),
			Containers:        safeGetByIndex(containersResults, i),
			Container:         safeGetByIndex(containerResults, i),
			PriorityClassName: safeGetByIndex(priorityClassNameResults, i),
			Image:             safeGetByIndex(imageResults, i),
		}
	}

	return fragmentedSpecs, nil
}

// ExtractStatus evaluates the status of the component based on the status definition.
func (a *Accessor) ExtractStatus(ctx context.Context, definition v1alpha1.ComponentDefinition) (*Status, error) {
	if definition.StatusDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have status definition", definition.Name))
	}

	statusDef := definition.StatusDefinition

	var phase *string
	if statusDef.PhaseDefinition != nil {
		phaseField := &v1alpha1.ValueAccessor{Read: &statusDef.PhaseDefinition.Read}
		var phases []string
		if err := extractOptionalVia(ctx, phaseField, false, a.runner, &phases); err != nil {
			return nil, fmt.Errorf("failed to extract phase: %w", err)
		}
		if len(phases) > 0 {
			phase = &phases[0]
		}
	}

	conditions, err := a.extractConditions(ctx, statusDef.ConditionsDefinition)
	if err != nil {
		return nil, err
	}

	matchedStatuses, err := matchStatus(ctx, a.runner, phase, conditions, statusDef.StatusMappings)
	if err != nil {
		return nil, fmt.Errorf("failed to match status: %w", err)
	}

	status := Status{
		Phase:           phase,
		Conditions:      conditions,
		MatchedStatuses: matchedStatuses,
	}

	return &status, nil
}

func (a *Accessor) extractConditions(ctx context.Context, condDef *v1alpha1.ConditionsDefinition) ([]Condition, error) {
	if condDef == nil {
		return []Condition{}, nil
	}

	conditionsField := &v1alpha1.ValueAccessor{Read: &condDef.Read}
	var extractedRawConditions [][]map[string]any
	if err := extractOptionalVia(ctx, conditionsField, false, a.runner, &extractedRawConditions); err != nil {
		return nil, fmt.Errorf("failed to extract conditions: %w", err)
	}

	// As Condition path is a path to a list of conditions, extract returns a slice of slice in size one/zero,
	// so we need to flatten it
	rawConditions := lo.Flatten(extractedRawConditions)

	conditions := make([]Condition, 0, len(rawConditions))
	for _, condMap := range rawConditions {
		cond := Condition{}

		if typeVal, ok := condMap[condDef.TypeFieldName]; ok {
			if typeStr, ok := typeVal.(string); ok {
				cond.Type = typeStr
			}
		}

		if statusVal, ok := condMap[condDef.StatusFieldName]; ok {
			if statusStr, ok := statusVal.(string); ok {
				cond.Status = &statusStr
			}
		}

		if condDef.MessageFieldName != nil {
			if msgVal, ok := condMap[*condDef.MessageFieldName]; ok {
				if msgStr, ok := msgVal.(string); ok {
					cond.Message = msgStr
				}
			}
		}

		if condDef.ReasonFieldName != nil {
			if reasonVal, ok := condMap[*condDef.ReasonFieldName]; ok {
				if reasonStr, ok := reasonVal.(string); ok {
					cond.Reason = &reasonStr
				}
			}
		}

		conditions = append(conditions, cond)
	}
	return conditions, nil
}

// ApplySuspendActions writes true to the declared suspend location.

// ApplyResumeActions writes false to the same declared location.

func (a *Accessor) ExtractInstanceIds(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]string, error) {
	if definition.InstanceIds == nil || definition.InstanceIds.Read == nil || definition.InstanceIds.Read.Expression == "" {
		return nil, DefinitionNotFoundError("no instance ids defined")
	}

	results, err := a.runner.EvaluateWithVariables(ctx, definition.InstanceIds.Read.Expression, nil)
	if err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("instance ids expression must return exactly one list, got %d results", len(results))
	}
	list, ok := results[0].([]any)
	if !ok {
		return nil, fmt.Errorf("instance ids expression must return a list, got %T", results[0])
	}

	var instanceIds []string
	seen := make(map[string]bool, len(list))
	for index, value := range list {
		id, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("instance ids must be strings, got %T at index %d", value, index)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate instance id %q at index %d", id, index)
		}
		seen[id] = true
		instanceIds = append(instanceIds, id)
	}

	// Validate all instance ids are not empty
	if lo.Contains(instanceIds, "") {
		return nil, fmt.Errorf("instance ids contained empty string values [%s]", strings.Join(instanceIds, ","))
	}

	return instanceIds, nil
}

// extractVia reads one field through the pair's expression. An instanced component's expression
// returns one value per instance in a list, which is spread into the stream shape the converters
// expect; a single-instance field keeps its one result, so a list-valued field such as containers
// survives intact.
func extractVia[T any](ctx context.Context, via *v1alpha1.ValueAccessor, instanced bool, runner expression.Runner, out *[]T) error {
	if via == nil || via.Read == nil || via.Read.Expression == "" {
		return nil
	}
	if via.Read.MatchExpression != "" {
		results, err := runner.EvaluateWithVariables(ctx, via.Read.MatchExpression, nil)
		if err != nil {
			return err
		}
		matched, err := booleanResult(results)
		if err != nil {
			return err
		}
		if !matched {
			return ErrValueNotFound
		}
	}
	results, err := runner.EvaluateWithVariables(ctx, via.Read.Expression, nil)
	if err != nil {
		return err
	}
	if instanced && len(results) == 1 {
		if list, ok := results[0].([]any); ok {
			results = list
		}
	}
	converted, err := safeConvertSlice[T](results)
	if err != nil {
		return err
	}
	*out = converted

	return nil
}

// instancedComponent reports whether the component holds one pod definition per instance, in
// which case a pair expression returns a per-instance list.
func instancedComponent(definition v1alpha1.ComponentDefinition) bool {
	return definition.InstanceIds != nil && definition.InstanceIds.Read != nil && definition.InstanceIds.Read.Expression != ""
}

// assignVia writes a field using SDK policy and the definition's destination.

func safeGetByIndex[T any](s []T, i int) T {
	var zero T
	if i < 0 || i >= len(s) {
		return zero
	}

	return s[i]
}

func safeConvertSlice[T any](slice []any) ([]T, error) {
	if slice == nil {
		return nil, nil
	}

	convertedResults := make([]T, len(slice))
	for i, object := range slice {
		// First try direct type assertion (for simple types)
		if converted, ok := object.(T); ok {
			convertedResults[i] = converted
			continue
		}

		// For complex types (structs), use JSON marshaling/unmarshaling
		var converted T
		if err := convertViaJSON(object, &converted); err != nil {
			return nil, fmt.Errorf("failed to convert object at index %d to type %T: %w", i, converted, err)
		}
		convertedResults[i] = converted
	}

	return convertedResults, nil
}

// convertViaJSON converts between types using JSON marshaling
func convertViaJSON(src any, dst any) error {
	jsonBytes, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("failed to marshal source: %w", err)
	}

	if err := json.Unmarshal(jsonBytes, dst); err != nil {
		return fmt.Errorf("failed to unmarshal to destination: %w", err)
	}

	return nil
}

func matchStatus(ctx context.Context, runner expression.Runner, phase *string, conditions []Condition, mappings v1alpha1.StatusMappings) ([]v1alpha1.ResourceStatus, error) {
	conditionsMap := make(map[string]Condition, len(conditions))
	for _, cond := range conditions {
		conditionsMap[cond.Type] = cond
	}

	matchedStatuses := make([]v1alpha1.ResourceStatus, 0)
	for _, entry := range mappings.Entries() {
		matched, err := evaluateMatchers(ctx, runner, phase, conditionsMap, entry.Matchers)
		if err != nil {
			return nil, err
		}
		if matched {
			matchedStatuses = append(matchedStatuses, entry.Status)
		}
	}

	if len(matchedStatuses) == 0 {
		return []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus}, nil
	}

	return matchedStatuses, nil
}

func evaluateMatchers(ctx context.Context, runner expression.Runner, phase *string, conditionsMap map[string]Condition, matchers []v1alpha1.StatusMatcher) (bool, error) {
	for _, matcher := range matchers {
		matched, err := match(ctx, runner, phase, conditionsMap, matcher)
		if err != nil {
			return false, fmt.Errorf("failed to evaluate matcher: %w", err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func match(ctx context.Context, runner expression.Runner, phase *string, conditionsMap map[string]Condition, matcher v1alpha1.StatusMatcher) (bool, error) {
	if matcher.ByPhase != "" {
		if phase == nil || *phase != matcher.ByPhase {
			return false, nil
		}
	}

	for _, expectedCond := range matcher.ByConditions {
		actualCond, found := checkCondition(conditionsMap, expectedCond)
		if !found {
			return false, nil
		}

		expectedValue := ptr.Deref(expectedCond.Status, "")
		if expectedValue != "" && expectedValue != ptr.Deref(actualCond.Status, "") {
			return false, nil
		}

		expectedValue = ptr.Deref(expectedCond.Reason, "")
		if expectedValue != "" && expectedValue != ptr.Deref(actualCond.Reason, "") {
			return false, nil
		}
	}

	if matcher.ByExpression != nil {
		matched, err := matchByExpression(ctx, runner, matcher)
		if err != nil {
			return false, err
		}

		return matched, nil
	}

	return true, nil
}

// matchByExpression evaluates a status matcher through the definition's own runner, so the
// variables stay bound. The result is compared to ExpectedResult in string form. The empty
// variable set binds value, instance and index to null; a matcher does not use them.
func matchByExpression(ctx context.Context, runner expression.Runner, matcher v1alpha1.StatusMatcher) (bool, error) {
	results, err := runner.EvaluateWithVariables(ctx, matcher.ByExpression.Expression, map[string]any{})
	if err != nil {
		return false, fmt.Errorf("failed to evaluate ByExpression: %w", err)
	}
	if len(results) == 0 {
		return false, nil
	}

	return booleanResult(results)
}

func checkCondition(conditionsMap map[string]Condition, expectedCond v1alpha1.ExpectedCondition) (Condition, bool) {
	actualCond, found := conditionsMap[expectedCond.Type]
	if !found {
		return Condition{}, false
	}
	return actualCond, true
}

// ErrValueNotFound means a read accessor does not apply to this workload.
var ErrValueNotFound = errors.New("read accessor does not match the workload")

func extractOptionalVia[T any](ctx context.Context, via *v1alpha1.ValueAccessor, instanced bool, runner expression.Runner, out *[]T) error {
	err := extractVia(ctx, via, instanced, runner, out)
	if errors.Is(err, ErrValueNotFound) {
		return nil
	}
	return err
}

func booleanResult(results []any) (bool, error) {
	if len(results) != 1 {
		return false, fmt.Errorf("match expression must return one boolean")
	}
	value, ok := results[0].(bool)
	if !ok {
		return false, fmt.Errorf("match expression must return boolean, got %T", results[0])
	}
	return value, nil
}
