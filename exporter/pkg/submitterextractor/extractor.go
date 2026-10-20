// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package submitterextractor evaluates submitter CEL against object metadata.
package submitterextractor

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/google/cel-go/cel"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	maxConfigBytes     = 256 * 1024
	maxExpressionBytes = 8192
	maxOutputBytes     = 512
	maxCachedResults   = 4096
	costLimit          = 10000
)

type Interface interface {
	Evaluate(context.Context, metav1.Object, schema.GroupVersionKind) (Submitter, error)
}

type Submitter struct {
	Identity string
}

// Extractor is safe for concurrent use. Callers must not mutate an object during
// Evaluate. Successful results are cached by CEL input, not resource version.
type Extractor struct {
	program cel.Program

	lock   sync.Mutex
	cache  map[[32]byte]*list.Element
	recent list.List
}

type cacheEntry struct {
	fingerprint [32]byte
	submitter   Submitter
}

var _ Interface = (*Extractor)(nil)

// New compiles one string-valued CEL expression. Empty expressions are invalid;
// the controller disables extraction by leaving its dependency unset.
func New(expression string) (Interface, error) {
	if expression == "" || len(expression) > maxExpressionBytes {
		return nil, fmt.Errorf("submitter expression must contain 1 to %d bytes", maxExpressionBytes)
	}
	env, err := cel.NewEnv(
		cel.Variable("labels", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("annotations", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("resource", cel.MapType(cel.StringType, cel.StringType)),
	)
	if err != nil {
		return nil, fmt.Errorf("create submitter CEL environment: %w", err)
	}
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compile submitter expression: %w", issues.Err())
	}
	if !ast.OutputType().IsExactType(cel.StringType) {
		return nil, fmt.Errorf("submitter expression must return string, got %s", ast.OutputType())
	}
	program, err := env.Program(ast, cel.CostLimit(costLimit), cel.InterruptCheckFrequency(100))
	if err != nil {
		return nil, fmt.Errorf("create submitter CEL program: %w", err)
	}
	return &Extractor{program: program, cache: make(map[[32]byte]*list.Element)}, nil
}

// Parse accepts the chart's JSON attribution configuration.
func Parse(data []byte) (Interface, error) {
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("submitter configuration exceeds %d bytes", maxConfigBytes)
	}
	var config struct {
		SubmittedByExpression string `json:"submittedByExpression"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode submitter configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("submitter configuration must contain one JSON object")
	}
	return New(config.SubmittedByExpression)
}

func (e *Extractor) Evaluate(ctx context.Context, obj metav1.Object, gvk schema.GroupVersionKind) (Submitter, error) {
	if err := ctx.Err(); err != nil {
		return Submitter{}, fmt.Errorf("evaluate submitter: %w", err)
	}
	objectValue := reflect.ValueOf(obj)
	if !objectValue.IsValid() || (objectValue.Kind() == reflect.Pointer && objectValue.IsNil()) {
		return Submitter{}, fmt.Errorf("evaluate submitter: object is nil")
	}
	inputHash := fingerprint(obj, gvk)
	e.lock.Lock()
	if cached, ok := e.cache[inputHash]; ok {
		e.recent.MoveToFront(cached)
		submitter := cached.Value.(cacheEntry).submitter
		e.lock.Unlock()
		if err := ctx.Err(); err != nil {
			return Submitter{}, fmt.Errorf("evaluate submitter: %w", err)
		}
		return submitter, nil
	}
	e.lock.Unlock()

	labels, annotations := obj.GetLabels(), obj.GetAnnotations()
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	value, _, err := e.program.ContextEval(ctx, map[string]any{
		"labels":      labels,
		"annotations": annotations,
		"resource": map[string]string{
			"namespace": obj.GetNamespace(),
			"name":      obj.GetName(),
			"group":     gvk.Group,
			"version":   gvk.Version,
			"kind":      gvk.Kind,
		},
	})
	if contextErr := ctx.Err(); contextErr != nil {
		return Submitter{}, fmt.Errorf("evaluate submitter: %w", contextErr)
	}
	if err != nil {
		return Submitter{}, fmt.Errorf("evaluate submitter CEL: %w", err)
	}
	result, ok := value.Value().(string)
	if !ok {
		return Submitter{}, fmt.Errorf("submitter expression returned %T, want string", value.Value())
	}
	if len(result) > maxOutputBytes || !utf8.ValidString(result) ||
		strings.IndexFunc(result, unicode.IsControl) != -1 || strings.TrimSpace(result) != result {
		return Submitter{}, fmt.Errorf("submitter identity must be valid UTF-8, at most %d bytes, without control characters or surrounding whitespace", maxOutputBytes)
	}
	// CEL can return a substring backed by a much larger metadata value.
	submitter := Submitter{Identity: strings.Clone(result)}
	e.lock.Lock()
	defer e.lock.Unlock()
	if cached, ok := e.cache[inputHash]; ok {
		e.recent.MoveToFront(cached)
		return submitter, nil
	}
	e.cache[inputHash] = e.recent.PushFront(cacheEntry{fingerprint: inputHash, submitter: submitter})
	if len(e.cache) > maxCachedResults {
		oldest := e.recent.Back()
		delete(e.cache, oldest.Value.(cacheEntry).fingerprint)
		e.recent.Remove(oldest)
	}
	return submitter, nil
}
