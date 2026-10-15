// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package karta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/dsx-ai-factory/workload-map/pkg/jq/execution"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

func (w *workload) UpdatePodTemplate(ctx context.Context, component string, update PodTemplateUpdate, opts ...UpdateOption) error {
	if isNilInput(update) {
		return ErrEmptyPatch
	}
	return w.patchPodTemplate(ctx, component, update.AsPodMergePatch(), opts...)
}

// instanceTargets carries the WithInstances resolution: selected is nil when
// the update applies to every instance.
type instanceTargets struct {
	selected map[string]bool
	all      []string // every instance id, in path evaluation order
}

// resolveTargets validates WithInstances: known ids, no duplicates, and the v1
// one-to-one iteration rule between instanceIdPath and every written path.
// Checking each physical route directly (instead of one shape-level base)
// makes per-instance targeting work on any shape whose written paths iterate
// alongside the instance ids - including fragmented ones.
func resolveTargets(ctx context.Context, component *resource.Component, options UpdateOptions, writes []pathWrite) (instanceTargets, error) {
	if len(options.Instances) == 0 {
		return instanceTargets{}, nil
	}
	ids, err := component.GetInstanceIds(ctx)
	if err != nil {
		return instanceTargets{}, fmt.Errorf("karta: get instance ids: %w", err)
	}
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		if known[id] {
			return instanceTargets{}, fmt.Errorf("karta: duplicate instance id %q on component %s", id, component.Name())
		}
		known[id] = true
	}
	set := make(map[string]bool, len(options.Instances))
	for _, id := range options.Instances {
		if !known[id] {
			return instanceTargets{}, fmt.Errorf("karta: unknown instance id %q for component %s", id, component.Name())
		}
		if set[id] {
			return instanceTargets{}, fmt.Errorf("karta: instance id %q requested twice", id)
		}
		set[id] = true
	}
	definition := component.Definition()
	if definition.InstanceIdPath == nil {
		return instanceTargets{}, fmt.Errorf("karta: WithInstances requires an instanceIdPath on component %s: %w", component.Name(), ErrNotSupported)
	}
	idSegments, ok := parseWritablePath(*definition.InstanceIdPath)
	if !ok || iterCount(idSegments) != 1 {
		return instanceTargets{}, fmt.Errorf("karta: WithInstances requires a writable single-iteration instanceIdPath on component %s: %w", component.Name(), ErrNotSupported)
	}
	for _, write := range writes {
		segments, ok := parseWritablePath(write.path)
		if !ok || !sharesIterationBase(idSegments, segments) {
			return instanceTargets{}, fmt.Errorf("karta: WithInstances requires %s and instanceIdPath to share one iteration base on component %s: %w",
				write.path, component.Name(), ErrNotSupported)
		}
	}
	return instanceTargets{selected: set, all: ids}, nil
}

// pathWrite is one jq assignment at one physical path: read the current
// values, transform each (or keep, when the instance is untargeted), write
// back.
type pathWrite struct {
	path      string
	transform func(current any) (any, error)
}

// applyWrite reads the current values at the write's path, transforms the
// targeted ones and assigns them back in the same evaluation order.
func applyWrite(ctx context.Context, runner execution.Runner, write pathWrite, instances instanceTargets) error {
	current, err := runner.Evaluate(ctx, write.path)
	if err != nil {
		return fmt.Errorf("karta: read %s: %w", write.path, err)
	}
	if len(current) == 0 {
		return fmt.Errorf("karta: path %s matched nothing on the object", write.path)
	}
	if instances.selected != nil && len(current) != len(instances.all) {
		return fmt.Errorf("karta: %s matched %d locations but the component has %d instances: %w",
			write.path, len(current), len(instances.all), ErrNotSupported)
	}
	if instances.selected != nil {
		// Reassigning an excluded nil result would create a previously absent
		// field. Concrete paths also preserve map keys for object iteration.
		locations, err := runner.Evaluate(ctx, "path("+write.path+")")
		if err != nil {
			return fmt.Errorf("karta: resolve paths for %s: %w", write.path, err)
		}
		if len(locations) != len(current) {
			return fmt.Errorf("karta: %s resolved %d paths for %d values: %w", write.path, len(locations), len(current), ErrNotSupported)
		}
		for i, value := range current {
			if !instances.selected[instances.all[i]] {
				continue
			}
			transformed, err := write.transform(value)
			if err != nil {
				return err
			}
			location, err := json.Marshal(locations[i])
			if err != nil {
				return fmt.Errorf("karta: encode path for %s: %w", write.path, err)
			}
			if err := runner.Assign(ctx, "getpath("+string(location)+")", transformed); err != nil {
				return fmt.Errorf("karta: write %s: %w", write.path, err)
			}
		}
		return nil
	}
	values := make([]any, len(current))
	for i, value := range current {
		transformed, err := write.transform(value)
		if err != nil {
			return err
		}
		values[i] = transformed
	}
	if err := runner.AssignZip(ctx, write.path, values); err != nil {
		return fmt.Errorf("karta: write %s: %w", write.path, err)
	}
	return nil
}

// mergeMap merges entries into the current raw map, creating it when
// absent. Untouched keys survive verbatim. Entry values are JSON-primitive
// strings - the strict pod-template decode guarantees it.
func mergeMap(entries map[string]any) func(any) (any, error) {
	return func(current any) (any, error) {
		merged := map[string]any{}
		if existing, ok := current.(map[string]any); ok {
			maps.Copy(merged, existing)
		} else if current != nil {
			return nil, fmt.Errorf("karta: existing value is %T, not an object", current)
		}
		maps.Copy(merged, entries)
		return merged, nil
	}
}

// toUnstructured owns the decoded JSON and rejects integer tokens that cannot
// survive the runner's float64 representation exactly.
func toUnstructured(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("karta: encode value: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("karta: decode value: %w", err)
	}
	return canonicalizeNumbers(raw)
}

func canonicalizeNumbers(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		if !strings.ContainsAny(string(typed), ".eE") {
			integer, err := typed.Int64()
			if err != nil || integer < -(1<<53) || integer > 1<<53 {
				return nil, fmt.Errorf("karta: integer %s is outside the supported range [-2^53, 2^53]", typed)
			}
		}
		number, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("karta: convert number %s: %w", typed, err)
		}
		return number, nil
	case map[string]any:
		for key, entry := range typed {
			converted, err := canonicalizeNumbers(entry)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			typed[key] = converted
		}
	case []any:
		for i, entry := range typed {
			converted, err := canonicalizeNumbers(entry)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			typed[i] = converted
		}
	}
	return value, nil
}
