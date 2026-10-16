// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ownership

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

type terminalCancelContext struct {
	context.Context
	cancel   context.CancelFunc
	calls    int
	cancelAt int
}

func (c *terminalCancelContext) Err() error {
	c.calls++
	if c.calls == c.cancelAt {
		c.cancel()
	}
	return c.Context.Err()
}

func TestTraceRootTerminalValidationMatchesTrace(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mutate   func(*Object)
		depth    int
		cancelAt int
		want     error
	}{
		{name: "nil owners"},
		{name: "empty owners", mutate: func(o *Object) { o.Owners = []metav1.OwnerReference{} }},
		{name: "passive owner", mutate: func(o *Object) { o.Owners = []metav1.OwnerReference{{Controller: ptr.To(false)}} }},
		{name: "implicit owner", mutate: func(o *Object) { o.Owners = []metav1.OwnerReference{{}} }},
		{name: "missing UID", mutate: func(o *Object) { o.Ref.UID = "" }, want: ErrInvalidReference},
		{name: "missing name", mutate: func(o *Object) { o.Ref.Name = "" }, want: ErrInvalidReference},
		{name: "invalid API version", mutate: func(o *Object) { o.Ref.GVK.Version = "invalid/version" }, want: ErrInvalidReference},
		{name: "invalid scope", mutate: func(o *Object) { o.Ref.Namespace = "" }, want: ErrScope},
		{name: "negative depth before invalid seed", depth: -1, mutate: func(o *Object) { o.Ref.UID = "" }, want: ErrInvalidReference},
		{name: "cancellation before depth and seed", depth: -1, cancelAt: 1, mutate: func(o *Object) { o.Ref.UID = "" }, want: context.Canceled},
		{name: "cancellation after seed validation", cancelAt: 2, want: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start := traceObject("seed", "team")
			if tt.mutate != nil {
				tt.mutate(&start)
			}
			var results [2]error
			var checks [2]int
			var path []Object
			var root Ref
			for mode := range 2 {
				ctx, cancel := context.WithCancel(context.Background())
				counted := &terminalCancelContext{Context: ctx, cancel: cancel, cancelAt: tt.cancelAt}
				lookup := func(context.Context, Ref) (Object, error) {
					t.Fatal("terminal object reached lookup")
					return Object{}, nil
				}
				if mode == 0 {
					path, results[mode] = Trace(counted, start, lookup, tt.depth)
				} else {
					root, results[mode] = TraceRoot(counted, start, lookup, tt.depth)
				}
				checks[mode] = counted.calls
				cancel()
			}
			if !reflect.DeepEqual(results[0], results[1]) || checks[0] != checks[1] || !errors.Is(results[1], tt.want) {
				t.Fatalf("terminal behavior differs: errors=%+v checks=%v, want %v", results, checks, tt.want)
			}
			if tt.want == nil {
				if !reflect.DeepEqual(path, []Object{start}) || root != start.Ref {
					t.Fatalf("terminal identity differs: path=%+v root=%+v", path, root)
				}
			} else if root != (Ref{}) {
				t.Fatalf("failed terminal root = %+v", root)
			}
		})
	}
}
