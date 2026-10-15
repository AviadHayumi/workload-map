// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ownership

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type traceRootCase struct {
	start        Object
	owner        Object
	depth        int
	cause        error
	nilLookup    bool
	cancelBefore bool
	cancelDuring bool
}

func TestTraceRootMatchesTrace(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*traceRootCase)
		want   error
	}{
		{name: "terminal seed at zero depth", mutate: func(c *traceRootCase) { c.start.Owners = nil; c.depth = 0 }},
		{name: "one owner"},
		{name: "served version alias", mutate: func(c *traceRootCase) { c.owner.Ref.GVK.Version = "v2" }},
		{name: "core API alias", mutate: func(c *traceRootCase) { c.start.Owners[0].APIVersion = "/v1"; c.owner.Ref.GVK.Group = "" }},
		{name: "cluster owner", mutate: func(c *traceRootCase) { c.owner.Ref.Namespace = ""; c.owner.Namespaced = false }},
		{name: "maximum depth", mutate: func(c *traceRootCase) { c.depth = int(^uint(0) >> 1) }},
		{name: "invalid seed", mutate: func(c *traceRootCase) { c.start.Ref.UID = "" }, want: ErrInvalidReference},
		{name: "invalid structured seed version", mutate: func(c *traceRootCase) { c.start.Ref.GVK.Version = "v1/extra" }, want: ErrInvalidReference},
		{name: "negative depth before invalid seed", mutate: func(c *traceRootCase) { c.depth = -1; c.start.Ref.UID = "" }, want: ErrInvalidReference},
		{name: "multiple controllers before version and depth", mutate: func(c *traceRootCase) {
			c.start.Owners = append(c.start.Owners, traceOwner("other"))
			c.start.Owners[0].APIVersion = "invalid/version/extra"
			c.depth = 0
		}, want: ErrMultipleControllers},
		{name: "invalid owner version before depth", mutate: func(c *traceRootCase) { c.start.Owners[0].APIVersion = "invalid/version/extra"; c.depth = 0 }, want: ErrInvalidReference},
		{name: "missing owner UID", mutate: func(c *traceRootCase) { c.start.Owners[0].UID = "" }, want: ErrInvalidReference},
		{name: "missing owner name", mutate: func(c *traceRootCase) { c.start.Owners[0].Name = "" }, want: ErrInvalidReference},
		{name: "depth before cycle", mutate: func(c *traceRootCase) { c.start.Owners[0] = traceOwner("seed"); c.depth = 0 }, want: ErrDepthExceeded},
		{name: "cycle", mutate: func(c *traceRootCase) { c.owner.Owners = append(c.owner.Owners, traceOwner("seed")) }, want: ErrCycle},
		{name: "nil lookup", mutate: func(c *traceRootCase) { c.nilLookup = true }, want: ErrInvalidReference},
		{name: "UID before invalid actual version", mutate: func(c *traceRootCase) { c.owner.Ref.UID = "replacement"; c.owner.Ref.GVK.Version = "" }, want: ErrUIDMismatch},
		{name: "invalid actual group", mutate: func(c *traceRootCase) { c.owner.Ref.GVK.Group = "invalid/group" }, want: ErrInvalidReference},
		{name: "different actual name", mutate: func(c *traceRootCase) { c.owner.Ref.Name = "other" }, want: ErrInvalidReference},
		{name: "different actual kind", mutate: func(c *traceRootCase) { c.owner.Ref.GVK.Kind = "Other" }, want: ErrInvalidReference},
		{name: "different actual group", mutate: func(c *traceRootCase) { c.owner.Ref.GVK.Group = "other.test" }, want: ErrInvalidReference},
		{name: "cross namespace", mutate: func(c *traceRootCase) { c.owner.Ref.Namespace = "other" }, want: ErrScope},
		{name: "cluster dependent", mutate: func(c *traceRootCase) { c.start.Ref.Namespace = ""; c.start.Namespaced = false }, want: ErrScope},
		{name: "actual scope mismatch", mutate: func(c *traceRootCase) { c.owner.Namespaced = false }, want: ErrScope},
		{name: "missing owner", mutate: func(c *traceRootCase) {
			c.cause = apierrors.NewNotFound(schema.GroupResource{Resource: "workloads"}, "owner")
		}, want: ErrMissing},
		{name: "forbidden owner", mutate: func(c *traceRootCase) {
			c.cause = fmt.Errorf("lookup owner: %w", apierrors.NewForbidden(schema.GroupResource{Resource: "workloads"}, "owner", errors.New("denied")))
		}},
		{name: "lookup deadline", mutate: func(c *traceRootCase) { c.cause = context.DeadlineExceeded }, want: context.DeadlineExceeded},
		{name: "cancellation before invalid depth and seed", mutate: func(c *traceRootCase) { c.cancelBefore = true; c.depth = -1; c.start.Ref.UID = "" }, want: context.Canceled},
		{name: "cancellation during lookup before UID", mutate: func(c *traceRootCase) { c.cancelDuring = true; c.owner.Ref.UID = "replacement" }, want: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := traceRootCase{start: traceObject("seed", "team", traceOwner("owner")), owner: traceObject("owner", "team"), depth: 32}
			if tt.mutate != nil {
				tt.mutate(&c)
			}
			var path []Object
			var root Ref
			var failures [2]error
			var requests [2][]Ref
			for run := range 2 {
				ctx, cancel := context.WithCancel(context.Background())
				if c.cancelBefore {
					cancel()
				}
				lookup := Lookup(func(_ context.Context, ref Ref) (Object, error) {
					requests[run] = append(requests[run], ref)
					if c.cancelDuring {
						cancel()
					}
					return c.owner, c.cause
				})
				if c.nilLookup {
					lookup = nil
				}
				if run == 0 {
					path, failures[run] = Trace(ctx, c.start, lookup, c.depth)
				} else {
					root, failures[run] = TraceRoot(ctx, c.start, lookup, c.depth)
				}
				cancel()
			}
			if !reflect.DeepEqual(requests[0], requests[1]) || !reflect.DeepEqual(failures[0], failures[1]) {
				t.Fatalf("trace/root differ: requests=%+v, trace error=%+v, root error=%+v", requests, failures[0], failures[1])
			}
			if (failures[1] != nil) != (tt.want != nil || c.cause != nil) {
				t.Fatalf("unexpected result: root=%+v, error=%v", root, failures[1])
			}
			if tt.want != nil && !errors.Is(failures[1], tt.want) {
				t.Fatalf("error = %v, want %v", failures[1], tt.want)
			}
			if c.cause != nil && !errors.Is(failures[1], c.cause) {
				t.Fatalf("lookup cause was lost: %v", failures[1])
			}
			if failures[1] != nil {
				if root != (Ref{}) {
					t.Fatalf("failed root = %+v", root)
				}
				return
			}
			if len(path) == 0 || root != path[len(path)-1].Ref {
				t.Fatalf("root=%+v, path=%+v", root, path)
			}
		})
	}
}

func TestTraceRootDeepPathAndRetainedFailure(t *testing.T) {
	objects := make(map[string]Object)
	for i := range 12 {
		name := fmt.Sprintf("object-%d", i)
		object := traceObject(name, "team")
		if i < 11 {
			object.Owners = append(object.Owners, traceOwner(fmt.Sprintf("object-%d", i+1)))
		}
		objects[name] = object
	}
	lookup := func(_ context.Context, ref Ref) (Object, error) { return objects[ref.Name], nil }
	start := objects["object-0"]
	for _, depth := range []int{0, 7, 8, 10, 11, 32} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			path, traceErr := Trace(context.Background(), start, lookup, depth)
			root, rootErr := TraceRoot(context.Background(), start, lookup, depth)
			if !reflect.DeepEqual(traceErr, rootErr) {
				t.Fatalf("trace error=%+v, root error=%+v", traceErr, rootErr)
			}
			if (rootErr != nil) != (depth < 11) {
				t.Fatalf("depth %d: root=%+v, error=%v", depth, root, rootErr)
			}
			if rootErr == nil {
				if root != path[len(path)-1].Ref {
					t.Fatalf("root=%+v, path=%+v", root, path)
				}
				return
			}
			var failure *Error
			if !errors.As(rootErr, &failure) || !errors.Is(rootErr, ErrDepthExceeded) || root != (Ref{}) {
				t.Fatalf("root=%+v, error=%v", root, rootErr)
			}
			preserved := append([]Ref(nil), failure.Path...)
			for range 3 {
				if _, err := TraceRoot(context.Background(), start, lookup, 32); err != nil {
					t.Fatal(err)
				}
			}
			path[0].Ref.Name = "changed"
			if !reflect.DeepEqual(failure.Path, preserved) {
				t.Fatalf("failure prefix changed: %+v", failure.Path)
			}
		})
	}
}

func BenchmarkTraceResult(b *testing.B) {
	start := traceObject("seed", "team", traceOwner("child"))
	child := traceObject("child", "team", traceOwner("root"))
	root := traceObject("root", "team")
	lookup := func(_ context.Context, ref Ref) (Object, error) {
		if ref.Name == "child" {
			return child, nil
		}
		return root, nil
	}
	b.Run("full-path", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			path, err := Trace(context.Background(), start, lookup, 32)
			if err != nil || len(path) != 3 {
				b.Fatalf("path=%+v, error=%v", path, err)
			}
		}
	})
	b.Run("root-reference", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ref, err := TraceRoot(context.Background(), start, lookup, 32)
			if err != nil || ref != root.Ref {
				b.Fatalf("root=%+v, error=%v", ref, err)
			}
		}
	})
}
