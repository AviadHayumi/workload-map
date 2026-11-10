// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package cel evaluates bounded, pure CEL expressions against native workloads.
package cel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/jq/execution"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

// Input owns an immutable snapshot of a workload and its root Pod template.
type Input struct {
	object       map[string]any
	template     map[string]any
	templatePath []string
}

type jsonBudget struct {
	bytes, nodes                 int
	maxBytes, maxNodes, maxDepth int
}

const (
	operationTimeout = 100 * time.Millisecond
	maxInputBytes    = 1 << 20
	maxInputNodes    = 65536
	maxInputDepth    = 64
	maxPathSegments  = 64
)

// NewInput snapshots a built-in catalog root with one string-keyed PodTemplateSpec path.
// The caller must not mutate object while the snapshot is being made.
func NewInput(ctx context.Context, object *unstructured.Unstructured) (*Input, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if object == nil || object.Object == nil {
		return nil, fmt.Errorf("workload object is nil")
	}
	budget := &jsonBudget{maxBytes: maxInputBytes, maxNodes: maxInputNodes, maxDepth: maxInputDepth}
	copied, err := copyJSON(ctx, object.Object, 1, budget)
	if err != nil {
		return nil, fmt.Errorf("snapshot workload: %w", err)
	}
	snapshot := copied.(map[string]any)
	gvk := (&unstructured.Unstructured{Object: snapshot}).GroupVersionKind()
	definition, err := catalog.Get(gvk)
	if err != nil {
		return nil, fmt.Errorf("resolve workload catalog: %w", err)
	}
	maxResults, timeoutMS := 2, 100
	runner := execution.NewRunner(snapshot, &maxResults, &timeoutMS)
	root, err := resource.NewComponentFactory(definition, resource.NewAccessor(runner)).GetRootComponent()
	if err != nil {
		return nil, fmt.Errorf("resolve root component: %w", err)
	}
	rootDefinition := root.Definition()
	kind := rootDefinition.Kind
	if kind == nil || kind.Group != gvk.Group || kind.Version != gvk.Version || kind.Kind != gvk.Kind {
		return nil, fmt.Errorf("catalog root kind does not match workload")
	}
	if rootDefinition.InstanceIdPath != nil || rootDefinition.SpecDefinition == nil || rootDefinition.SpecDefinition.PodTemplateSpecPath == nil {
		return nil, fmt.Errorf("catalog root does not define one PodTemplateSpec")
	}
	path, err := resolveTemplatePath(ctx, runner, *rootDefinition.SpecDefinition.PodTemplateSpecPath)
	if err != nil {
		return nil, err
	}
	var template any = snapshot
	for _, key := range path {
		parent, ok := template.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("template path traverses a non-map value")
		}
		template, ok = parent[key]
		if !ok {
			return nil, fmt.Errorf("template path is missing")
		}
	}
	if _, ok := template.(map[string]any); !ok {
		return nil, fmt.Errorf("PodTemplateSpec must be a map")
	}
	// Only path discovery uses jq. Values are copied from the raw snapshot to preserve integers.
	templateCopy, err := copyJSON(ctx, template, 1, budget)
	if err != nil {
		return nil, fmt.Errorf("snapshot template: %w", err)
	}
	metadata, ok := snapshot["metadata"].(map[string]any)
	if !ok {
		if _, exists := snapshot["metadata"]; exists {
			return nil, fmt.Errorf("workload metadata must be a map")
		}
		metadata = map[string]any{}
		budget.nodes += 2
		snapshot["metadata"] = metadata
	}
	if _, exists := metadata["annotations"]; !exists {
		budget.nodes += 2
		metadata["annotations"] = map[string]any{}
	}
	if budget.nodes > budget.maxNodes {
		return nil, fmt.Errorf("normalized activation exceeds node limit")
	}
	input := &Input{object: snapshot, template: templateCopy.(map[string]any), templatePath: path}
	encoded, err := json.Marshal(map[string]any{"object": input.object, "template": input.template})
	if err != nil {
		return nil, fmt.Errorf("encode activation: %w", err)
	}
	if len(encoded) > maxInputBytes {
		return nil, fmt.Errorf("activation exceeds %d bytes", maxInputBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return input, nil
}

// TemplatePath returns a detached copy of the root template's native map path.
func (i *Input) TemplatePath() []string {
	return append([]string(nil), i.templatePath...)
}

func resolveTemplatePath(ctx context.Context, runner execution.Runner, expression string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results, err := runner.Evaluate(ctx, "path("+expression+")")
	if err != nil {
		return nil, fmt.Errorf("resolve template path: %w", err)
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("expected exactly one template path")
	}
	segments, ok := results[0].([]any)
	if !ok || len(segments) == 0 || len(segments) > maxPathSegments {
		return nil, fmt.Errorf("template path must contain 1..%d map keys", maxPathSegments)
	}
	path := make([]string, len(segments))
	for index, segment := range segments {
		key, ok := segment.(string)
		if !ok {
			return nil, fmt.Errorf("template path contains a non-string key")
		}
		path[index] = key
	}
	return path, ctx.Err()
}

func (b *jsonBudget) visit(ctx context.Context, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.nodes++
	if b.nodes > b.maxNodes || depth > b.maxDepth {
		return fmt.Errorf("JSON traversal exceeds node or depth limit")
	}
	return nil
}

func (b *jsonBudget) addBytes(size int) error {
	if size > b.maxBytes-b.bytes {
		return fmt.Errorf("JSON value exceeds %d bytes", b.maxBytes)
	}
	b.bytes += size
	return nil
}

func (b *jsonBudget) scalar(value any) error {
	if value, ok := value.(string); ok {
		if !utf8.ValidString(value) {
			return fmt.Errorf("JSON string contains invalid UTF-8")
		}
		if len(value) > b.maxBytes-b.bytes {
			return fmt.Errorf("JSON string exceeds byte limit")
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("invalid JSON scalar: %w", err)
	}
	return b.addBytes(len(encoded))
}

func copyJSON(ctx context.Context, value any, depth int, budget *jsonBudget) (any, error) {
	if err := budget.visit(ctx, depth); err != nil {
		return nil, err
	}
	switch value := value.(type) {
	case map[string]any:
		if value == nil {
			return nil, budget.scalar(nil)
		}
		if len(value) > (budget.maxNodes-budget.nodes)/2 {
			return nil, fmt.Errorf("JSON map exceeds node limit")
		}
		encodedSize := 2
		if len(value) > 0 {
			encodedSize += 2*len(value) - 1
		}
		if err := budget.addBytes(encodedSize); err != nil {
			return nil, err
		}
		result := make(map[string]any, len(value))
		for key, child := range value {
			if err := budget.visit(ctx, depth+1); err != nil {
				return nil, err
			}
			if err := budget.scalar(key); err != nil {
				return nil, err
			}
			copied, err := copyJSON(ctx, child, depth+1, budget)
			if err != nil {
				return nil, err
			}
			result[key] = copied
		}
		return result, nil
	case []any:
		if value == nil {
			return nil, budget.scalar(nil)
		}
		if len(value) > budget.maxNodes-budget.nodes {
			return nil, fmt.Errorf("JSON list exceeds node limit")
		}
		encodedSize := 2
		if len(value) > 0 {
			encodedSize += len(value) - 1
		}
		if err := budget.addBytes(encodedSize); err != nil {
			return nil, err
		}
		result := make([]any, len(value))
		for index, child := range value {
			copied, err := copyJSON(ctx, child, depth+1, budget)
			if err != nil {
				return nil, err
			}
			result[index] = copied
		}
		return result, nil
	case nil, bool, string, int64, float64:
		if err := budget.scalar(value); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported JSON input type %T", value)
	}
}
