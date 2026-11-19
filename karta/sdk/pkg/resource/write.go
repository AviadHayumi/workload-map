// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	jsonutil "k8s.io/apimachinery/pkg/util/json"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// WriteTarget is a definition-owned destination resolved on the starting workload.
type WriteTarget struct {
	Path   string
	Exists bool
	Value  any
}

func (a *Accessor) ResolveWriteTarget(ctx context.Context, via *v1alpha1.ValueAccessor, instance string, index int) (WriteTarget, error) {
	if via == nil || via.Write == nil {
		return WriteTarget{}, fmt.Errorf("field is read-only")
	}
	write := via.Write
	if (write.Path == nil) == (write.PathExpression == "") {
		return WriteTarget{}, fmt.Errorf("write requires exactly one path or pathExpression")
	}
	variables := map[string]any{"instance": instance, "index": index}
	if write.MatchExpression != "" {
		result, err := a.runner.EvaluateWritePath(ctx, write.MatchExpression, variables)
		if err != nil {
			return WriteTarget{}, fmt.Errorf("write match: %w", err)
		}
		match, err := booleanResult(result)
		if err != nil {
			return WriteTarget{}, err
		}
		if !match {
			return WriteTarget{}, fmt.Errorf("write accessor does not match the workload")
		}
	}
	var path string
	if write.Path != nil {
		path = *write.Path
	} else {
		result, err := a.runner.EvaluateWritePath(ctx, write.PathExpression, variables)
		if err != nil {
			return WriteTarget{}, fmt.Errorf("write path: %w", err)
		}
		if len(result) != 1 {
			return WriteTarget{}, fmt.Errorf("pathExpression must return one JSON Pointer string")
		}
		var ok bool
		path, ok = result[0].(string)
		if !ok {
			return WriteTarget{}, fmt.Errorf("pathExpression must return a JSON Pointer string, got %T", result[0])
		}
	}
	parts, err := pointerSegments(path)
	if err != nil {
		return WriteTarget{}, err
	}
	object, err := a.runner.GetObject()
	if err != nil {
		return WriteTarget{}, err
	}
	value, exists, err := readPointer(object, parts)
	if err != nil {
		return WriteTarget{}, err
	}
	value, err = jsonCopy(value)
	if err != nil {
		return WriteTarget{}, err
	}
	return WriteTarget{Path: path, Exists: exists, Value: value}, nil
}

func applyWrites(ctx context.Context, object map[string]any, targets []WriteTarget, values []any, assignment bool) (map[string]any, error) {
	if len(targets) != len(values) {
		return nil, fmt.Errorf("write target and value counts differ")
	}
	parts := make([][]string, len(targets))
	patches := make([]any, len(values))
	for i, target := range targets {
		var err error
		parts[i], err = pointerSegments(target.Path)
		if err != nil {
			return nil, err
		}
		for j := 0; j < i; j++ {
			if !assignment && pointersOverlap(parts[j], parts[i]) {
				return nil, fmt.Errorf("overlapping write targets %q and %q", targets[j].Path, target.Path)
			}
		}
		patches[i], err = jsonCopy(values[i])
		if err != nil {
			return nil, fmt.Errorf("write %d: %w", i, err)
		}
		if patches[i] == nil && !assignment {
			return nil, fmt.Errorf("write %d: top-level null is not a supported patch", i)
		}
	}
	scratch, err := jsonCopy(object)
	if err != nil {
		return nil, err
	}
	for i, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, _, err := readPointer(scratch, parts[i])
		if err != nil {
			return nil, err
		}
		value := patches[i]
		if !assignment {
			value = mergePatch(current, value)
		}
		scratch, err = writePointer(scratch, parts[i], value)
		if err != nil {
			return nil, fmt.Errorf("write at %q: %w", target.Path, err)
		}
	}
	updated, ok := scratch.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workload must remain an object")
	}
	if err := validateKubernetesObject(&unstructured.Unstructured{Object: updated}); err != nil {
		return nil, err
	}
	for _, path := range []string{"/apiVersion", "/kind", "/metadata/name", "/metadata/namespace", "/metadata/uid", "/metadata/generateName"} {
		segments, err := pointerSegments(path)
		if err != nil {
			return nil, err
		}
		before, hadBefore, err := readPointer(object, segments)
		if err != nil {
			return nil, err
		}
		after, hasAfter, err := readPointer(updated, segments)
		if err != nil {
			return nil, err
		}
		if hadBefore != hasAfter || !reflect.DeepEqual(before, after) {
			return nil, fmt.Errorf("write changes workload identity at %s", path)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return updated, nil
}

func mergePatch(current, patch any) any {
	fields, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	result, ok := current.(map[string]any)
	if !ok {
		result = map[string]any{}
	}
	for key, value := range fields {
		if value == nil {
			delete(result, key)
		} else {
			result[key] = mergePatch(result[key], value)
		}
	}
	return result
}

func jsonCopy(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result any
	if err := jsonutil.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func pointerSegments(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("write path %q must be an RFC 6901 JSON Pointer", path)
	}
	parts := strings.Split(path[1:], "/")
	if len(parts) > 128 {
		return nil, fmt.Errorf("write path exceeds 128 segments")
	}
	for i, part := range parts {
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				continue
			}
			if j+1 >= len(part) || (part[j+1] != '0' && part[j+1] != '1') {
				return nil, fmt.Errorf("invalid JSON Pointer escape in %q", path)
			}
			j++
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func pointerIndex(part string, length int) (int, error) {
	index, err := strconv.Atoi(part)
	if err != nil || index < 0 || index >= length || strconv.Itoa(index) != part {
		return 0, fmt.Errorf("%q is not an existing array index", part)
	}
	return index, nil
}

func readPointer(object any, parts []string) (any, bool, error) {
	current := object
	for _, part := range parts {
		switch node := current.(type) {
		case map[string]any:
			var exists bool
			current, exists = node[part]
			if !exists {
				return nil, false, nil
			}
		case []any:
			index, err := pointerIndex(part, len(node))
			if err != nil {
				return nil, false, err
			}
			current = node[index]
		default:
			return nil, false, fmt.Errorf("cannot traverse %q through %T", part, node)
		}
	}
	return current, true, nil
}

func writePointer(object any, parts []string, value any) (any, error) {
	if len(parts) == 0 {
		return value, nil
	}
	part := parts[0]
	switch node := object.(type) {
	case map[string]any:
		if len(parts) == 1 {
			node[part] = value
			return node, nil
		}
		child, exists := node[part]
		if !exists {
			if _, err := strconv.Atoi(parts[1]); err == nil || parts[1] == "-" {
				return nil, fmt.Errorf("cannot infer a missing array parent")
			}
			child = map[string]any{}
		}
		next, err := writePointer(child, parts[1:], value)
		if err != nil {
			return nil, err
		}
		node[part] = next
		return node, nil
	case []any:
		index, err := pointerIndex(part, len(node))
		if err != nil {
			return nil, err
		}
		next, err := writePointer(node[index], parts[1:], value)
		if err != nil {
			return nil, err
		}
		node[index] = next
		return node, nil
	default:
		return nil, fmt.Errorf("cannot traverse %q through %T", part, object)
	}
}

func pointersOverlap(a, b []string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (a *Accessor) ApplySuspendActions(ctx context.Context, definition v1alpha1.ComponentDefinition) error {
	if definition.SuspendDefinition == nil {
		return DefinitionNotFoundError("component has no suspend definition")
	}
	return a.applyActions(ctx, definition, definition.SuspendDefinition.SuspendActions)
}

func (a *Accessor) ApplyResumeActions(ctx context.Context, definition v1alpha1.ComponentDefinition) error {
	if definition.SuspendDefinition == nil {
		return DefinitionNotFoundError("component has no suspend definition")
	}
	return a.applyActions(ctx, definition, definition.SuspendDefinition.ResumeActions)
}

func (a *Accessor) resolveActions(ctx context.Context, definition v1alpha1.ComponentDefinition, actions []v1alpha1.SuspendAction) ([]WriteTarget, []any, error) {
	ids := []string{""}
	if instancedComponent(definition) {
		var err error
		ids, err = a.ExtractInstanceIds(ctx, definition)
		if err != nil {
			return nil, nil, err
		}
	}
	var targets []WriteTarget
	var values []any
	for index, id := range ids {
		for _, action := range actions {
			target, err := a.ResolveWriteTarget(ctx, &v1alpha1.ValueAccessor{Write: &action.Write}, id, index)
			if err != nil {
				return nil, nil, err
			}
			var value any
			if err := jsonutil.Unmarshal([]byte(action.Value), &value); err != nil {
				return nil, nil, fmt.Errorf("decode action value: %w", err)
			}
			targets = append(targets, target)
			values = append(values, value)
		}
	}
	return targets, values, nil
}

func (a *Accessor) applyActions(ctx context.Context, definition v1alpha1.ComponentDefinition, actions []v1alpha1.SuspendAction) error {
	targets, values, err := a.resolveActions(ctx, definition, actions)
	if err != nil {
		return err
	}
	object, err := a.GetObject()
	if err != nil {
		return err
	}
	updated, err := applyWrites(ctx, object, targets, values, true)
	if err != nil {
		return err
	}
	return a.runner.Assign(ctx, "", updated)
}
