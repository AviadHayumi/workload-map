// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package execution

import (
	"container/list"
	"sync"
	"weak"

	"github.com/itchyny/gojq"
)

type compiledProgram struct {
	code *gojq.Code
	uses int
}

type programCache struct {
	mu    sync.Mutex
	idle  map[string]*list.Element
	order list.List
}

type idleProgram struct {
	expression string
	program    weak.Pointer[compiledProgram]
}

const (
	maxIdlePrograms = 256
	maxProgramUses  = 16
)

var programs programCache

// Taking removes the only idle slot before evaluation. Concurrent callers must
// compile independently, since gojq constants can escape through mutable results.
func (c *programCache) take(expression string) *compiledProgram {
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.idle[expression]
	if element == nil {
		return nil
	}
	delete(c.idle, expression)
	c.order.Remove(element)
	program := element.Value.(idleProgram).program.Value()
	if program != nil {
		program.uses++
	}
	return program
}

// Weak idle references let GC discard compiler state. The use limit also bounds
// reuse of gojq's internal dynamic-regexp cache between collections.
func (c *programCache) put(expression string, program *compiledProgram) {
	if program.uses >= maxProgramUses {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element := c.idle[expression]; element != nil {
		if element.Value.(idleProgram).program.Value() != nil {
			return
		}
		delete(c.idle, expression)
		c.order.Remove(element)
	}
	if c.idle == nil {
		c.idle = make(map[string]*list.Element)
	}
	c.idle[expression] = c.order.PushFront(idleProgram{expression: expression, program: weak.Make(program)})
	if c.order.Len() > maxIdlePrograms {
		oldest := c.order.Back()
		delete(c.idle, oldest.Value.(idleProgram).expression)
		c.order.Remove(oldest)
	}
}

func hasOnlyScalarResults(results []any) bool {
	for _, result := range results {
		switch result.(type) {
		case nil, bool, int, float64, string:
		default:
			return false
		}
	}
	return true
}
