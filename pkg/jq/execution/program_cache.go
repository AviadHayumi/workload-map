// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package execution

import (
	"container/list"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/itchyny/gojq"
)

type programCache struct {
	mu          sync.Mutex
	entries     map[programKey]*list.Element
	recency     list.List
	capacity    int
	maxKeyBytes int
}

type programKey struct {
	expression string
	variables  string
}

type cachedProgram struct {
	key  programKey
	code *gojq.Code
}

const (
	programCacheCapacity    = 128
	programCacheMaxKeyBytes = 16 * 1024
)

var compiledPrograms = newProgramCache(programCacheCapacity, programCacheMaxKeyBytes)

func newProgramCache(capacity, maxKeyBytes int) *programCache {
	return &programCache{entries: make(map[programKey]*list.Element), capacity: capacity, maxKeyBytes: maxKeyBytes}
}

func (c *programCache) compile(expression string, variables []string) (*gojq.Code, error) {
	key, cacheable := compilationKey(expression, variables, c.maxKeyBytes)
	cacheable = cacheable && c.capacity > 0
	if cacheable {
		c.mu.Lock()
		if entry := c.entries[key]; entry != nil {
			c.recency.MoveToFront(entry)
			code := entry.Value.(cachedProgram).code
			c.mu.Unlock()
			return code, nil
		}
		c.mu.Unlock()

		// A short expression or variable name may refer to a much larger backing
		// string. Compiled programs retain only their own bounded source strings.
		expression = strings.Clone(expression)
		key.expression = expression
		owned := make([]string, len(variables))
		for i, name := range variables {
			owned[i] = strings.Clone(name)
		}
		variables = owned
	}
	parsed, err := gojq.Parse(expression)
	if err != nil {
		return nil, &JQParseError{Expression: expression, Err: err}
	}
	code, err := gojq.Compile(parsed, gojq.WithVariables(variables))
	if err != nil {
		return nil, &JQCompileError{Expression: expression, Err: err}
	}
	if !cacheable || !isReusableQuery(parsed, variables) {
		return code, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[key]; entry != nil {
		c.recency.MoveToFront(entry)
		return entry.Value.(cachedProgram).code, nil
	}
	entry := c.recency.PushFront(cachedProgram{key: key, code: code})
	c.entries[key] = entry
	if c.recency.Len() > c.capacity {
		oldest := c.recency.Back()
		delete(c.entries, oldest.Value.(cachedProgram).key)
		c.recency.Remove(oldest)
	}
	return code, nil
}

func compilationKey(expression string, variables []string, maxBytes int) (programKey, bool) {
	if len(expression) > maxBytes {
		return programKey{}, false
	}
	var names strings.Builder
	for _, name := range variables {
		length := strconv.Itoa(len(name))
		if len(length)+1+len(name) > maxBytes-len(expression)-names.Len() {
			return programKey{}, false
		}
		names.WriteString(length)
		names.WriteByte(':')
		names.WriteString(name)
	}
	return programKey{expression: expression, variables: names.String()}, true
}

// gojq may return mutable compiled constants and retain runtime regular
// expressions. Only selectors with immutable constants can share compiled code
// without changing result ownership or retaining data from previous inputs.
func isReusableQuery(query *gojq.Query, variables []string) bool {
	if query == nil || query.Meta != nil || len(query.Imports) != 0 || len(query.FuncDefs) != 0 || len(query.Patterns) != 0 {
		return false
	}
	if query.Term == nil {
		return (query.Op == gojq.OpPipe || query.Op == gojq.OpComma) &&
			isReusableQuery(query.Left, variables) && isReusableQuery(query.Right, variables)
	}
	term := query.Term
	switch term.Type {
	case gojq.TermTypeIdentity, gojq.TermTypeNull, gojq.TermTypeTrue, gojq.TermTypeFalse:
	case gojq.TermTypeIndex:
		if !isReusableIndex(term.Index, variables) {
			return false
		}
	case gojq.TermTypeString:
		if term.Str == nil || term.Str.Queries != nil {
			return false
		}
	case gojq.TermTypeNumber:
		if _, err := strconv.Atoi(term.Number); err != nil {
			return false
		}
	case gojq.TermTypeFunc:
		// Variables use the function AST node; $ENV instead compiles to a map.
		if term.Func == nil || len(term.Func.Args) != 0 || !slices.Contains(variables, term.Func.Name) || term.Func.Name == "$ENV" {
			return false
		}
	case gojq.TermTypeQuery:
		if !isReusableQuery(term.Query, variables) {
			return false
		}
	default:
		return false
	}
	for _, suffix := range term.SuffixList {
		if suffix.Index != nil && !isReusableIndex(suffix.Index, variables) {
			return false
		}
	}
	return true
}

func isReusableIndex(index *gojq.Index, variables []string) bool {
	if index == nil || index.IsSlice || index.End != nil {
		return false
	}
	if index.Name != "" {
		return true
	}
	if index.Str != nil {
		return index.Str.Queries == nil
	}
	return isReusableQuery(index.Start, variables)
}
