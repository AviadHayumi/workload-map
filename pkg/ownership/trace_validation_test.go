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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestValidateRefMatchesGroupVersionRoundTrip(t *testing.T) {
	groups := []string{"", "apps", "/", "apps/x", " ", "例"}
	versions := []string{"", "v1", "/", "/v1", "v1/", "v1/x", " ", "例"}
	for _, group := range groups {
		for _, version := range versions {
			t.Run(fmt.Sprintf("%q/%q", group, version), func(t *testing.T) {
				ref := traceObject("seed", "team").Ref
				ref.GVK.Group = group
				ref.GVK.Version = version
				gv := ref.GVK.GroupVersion()
				parsed, err := schema.ParseGroupVersion(gv.String())
				wantValid := version != "" && err == nil && parsed == gv
				if got := validateRef(ref); (got == nil) != wantValid {
					t.Fatalf("validateRef(%+v) = %v, round trip valid = %t", ref, got, wantValid)
				}
			})
		}
	}
}

func TestTraceOwnerVersionDiagnostics(t *testing.T) {
	for _, version := range []string{"", "/", "apps/", "apps/v1/extra", "/v1"} {
		t.Run(version, func(t *testing.T) {
			owner := traceOwner("owner")
			owner.APIVersion = version
			start := traceObject("seed", "team", owner)
			gv, parseErr := schema.ParseGroupVersion(version)
			expected := Ref{UID: owner.UID, Name: owner.Name, Namespace: "team", GVK: gv.WithKind(owner.Kind)}
			reads := 0
			path, err := Trace(context.Background(), start, func(_ context.Context, ref Ref) (Object, error) {
				reads++
				if ref != expected {
					t.Fatalf("lookup = %+v, want %+v", ref, expected)
				}
				return Object{Ref: ref, Namespaced: true}, nil
			}, 1)
			if version == "/v1" {
				if err != nil || reads != 1 || len(path) != 2 || path[1].Ref.GVK.Group != "" || path[1].Ref.GVK.Version != "v1" {
					t.Fatalf("core version alias: path=%+v, error=%v, reads=%d", path, err, reads)
				}
				return
			}
			var failure *Error
			if !errors.Is(err, ErrInvalidReference) || !errors.As(err, &failure) || reads != 0 {
				t.Fatalf("invalid version: error=%v, reads=%d", err, reads)
			}
			if !reflect.DeepEqual(path, []Object{start}) || !reflect.DeepEqual(failure.Path, []Ref{start.Ref}) || failure.Expected != expected || failure.Actual != (Ref{}) {
				t.Fatalf("diagnostics: path=%+v, error=%+v", path, failure)
			}
			if (parseErr == nil) != (failure.Cause == nil) || parseErr != nil && (reflect.TypeOf(failure.Cause) != reflect.TypeOf(parseErr) || failure.Cause.Error() != parseErr.Error()) {
				t.Fatalf("cause = %v, want native parse error %v", failure.Cause, parseErr)
			}
		})
	}
}

func TestTraceInitialValidationPrecedence(t *testing.T) {
	start := traceObject("seed", "team", traceOwner("owner"), traceOwner("other"))
	start.Ref.GVK.Group = "invalid/group"
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tt := range []struct {
		name   string
		ctx    context.Context
		depth  int
		kind   error
		cause  error
		actual Ref
	}{
		{name: "cancellation before depth and seed", ctx: canceled, depth: -1, cause: context.Canceled},
		{name: "negative depth before seed", ctx: context.Background(), depth: -1, kind: ErrInvalidReference},
		{name: "invalid seed before controllers", ctx: context.Background(), depth: 0, kind: ErrInvalidReference, actual: start.Ref},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, err := Trace(tt.ctx, start, func(context.Context, Ref) (Object, error) {
				t.Fatal("invalid seed reached lookup")
				return Object{}, nil
			}, tt.depth)
			var failure *Error
			if !errors.As(err, &failure) || path != nil || failure.Kind != tt.kind || failure.Expected != start.Ref || failure.Actual != tt.actual || len(failure.Path) != 0 {
				t.Fatalf("initial validation: path=%+v, error=%+v", path, err)
			}
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Fatalf("error = %v, want cause %v", err, tt.cause)
			}
			if tt.name == "negative depth before seed" && (failure.Cause == nil || failure.Cause.Error() != "maxDepth must not be negative") {
				t.Fatalf("negative depth cause = %v", failure.Cause)
			}
		})
	}
}

func TestTracePathGrowthAndDepthBoundaries(t *testing.T) {
	objects := make(map[string]Object)
	want := make([]Object, 7)
	for i := range want {
		name := fmt.Sprintf("object-%d", i)
		want[i] = traceObject(name, "team")
		if i < len(want)-1 {
			want[i] = traceObject(name, "team", traceOwner(fmt.Sprintf("object-%d", i+1)))
		}
		objects[name] = want[i]
	}
	for _, depth := range []int{0, 1, 3, 4, int(^uint(0) >> 1)} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			reads := 0
			path, err := Trace(context.Background(), want[0], func(_ context.Context, ref Ref) (Object, error) {
				reads++
				return objects[ref.Name], nil
			}, depth)
			count := min(depth, len(want)-1) + 1
			if !reflect.DeepEqual(path, want[:count]) || reads != count-1 {
				t.Fatalf("path=%+v, reads=%d, want prefix length %d", path, reads, count)
			}
			if count == len(want) {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure *Error
			if !errors.Is(err, ErrDepthExceeded) || !errors.As(err, &failure) || failure.Expected != want[count].Ref || failure.Actual != (Ref{}) || len(failure.Path) != count {
				t.Fatalf("depth error = %+v", err)
			}
			for i := range failure.Path {
				if failure.Path[i] != want[i].Ref {
					t.Fatalf("verified prefix[%d] = %+v", i, failure.Path[i])
				}
			}
			path[0].Ref.Name = "changed"
			if failure.Path[0] != want[0].Ref {
				t.Fatal("error prefix aliases returned path")
			}
		})
	}
}

func TestIndexPreservesParsedOwnerAtBoundary(t *testing.T) {
	for _, version := range []string{"", "/", "apps/", "apps/v1/extra", "/v1", "apps/v1"} {
		t.Run(version, func(t *testing.T) {
			owner := traceOwner("owner")
			owner.APIVersion = version
			var actual schema.GroupVersionKind
			calls := 0
			index := New()
			result := index.Resolve([]metav1.OwnerReference{owner}, func(schema.GroupKind) bool {
				t.Fatal("ignored owner reached root selection")
				return false
			}, func(gvk schema.GroupVersionKind) bool {
				actual = gvk
				calls++
				return true
			}, nil)
			if result.Outcome != OutcomeNoController || result.Resolution != "ignored_owner" || actual != refGVK(&owner) || calls != 1 {
				t.Fatalf("legacy boundary: result=%+v, kind=%+v, calls=%d", result, actual, calls)
			}
		})
	}
}

func TestTraceCycleDetectionAcrossScratchBoundary(t *testing.T) {
	for _, length := range []int{1, 16, 17, 18, 65} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			objects := make([]Object, length)
			for i := range objects {
				objects[i] = traceObject(fmt.Sprint(i), "team")
				if i+1 < length {
					objects[i].Owners = []metav1.OwnerReference{traceOwner(fmt.Sprint(i + 1))}
				}
			}
			for _, target := range []int{0, length / 2, length - 1} {
				objects[length-1].Owners = []metav1.OwnerReference{traceOwner(fmt.Sprint(target))}
				reads := 0
				lookup := func(_ context.Context, ref Ref) (Object, error) {
					reads++
					for i := range objects {
						if objects[i].Ref.UID == ref.UID {
							return objects[i], nil
						}
					}
					t.Fatalf("unexpected lookup: %+v", ref)
					return Object{}, nil
				}
				path, err := Trace(context.Background(), objects[0], lookup, length+1)
				var failure *Error
				if !errors.Is(err, ErrCycle) || !errors.As(err, &failure) || len(path) != length || len(failure.Path) != length || reads != length-1 || failure.Expected != objects[target].Ref {
					t.Fatalf("target %d: path=%+v, error=%+v, reads=%d", target, path, err, reads)
				}
				reads = 0
				root, rootErr := TraceRoot(context.Background(), objects[0], lookup, length+1)
				if root != (Ref{}) || !reflect.DeepEqual(err, rootErr) || reads != length-1 {
					t.Fatalf("root traversal differs: root=%+v, error=%v, reads=%d", root, rootErr, reads)
				}
			}
			objects[length-1].Owners = nil
			lookup := func(_ context.Context, ref Ref) (Object, error) {
				for i := range objects {
					if objects[i].Ref.UID == ref.UID {
						return objects[i], nil
					}
				}
				t.Fatalf("unexpected lookup: %+v", ref)
				return Object{}, nil
			}
			path, err := Trace(context.Background(), objects[0], lookup, int(^uint(0)>>1))
			if err != nil || !reflect.DeepEqual(path, objects) {
				t.Fatalf("successful path=%+v, error=%v", path, err)
			}
			path[0].Ref.Name = "caller-mutated"
			second, err := Trace(context.Background(), objects[0], lookup, length-1)
			if err != nil || !reflect.DeepEqual(second, objects) || path[0].Ref.Name != "caller-mutated" {
				t.Fatalf("returned path aliases a later traversal: path=%+v, error=%v", second, err)
			}
		})
	}
}

func TestTraceLongFailuresPreserveVerifiedPrefix(t *testing.T) {
	for _, name := range []string{"missing", "forbidden", "canceled", "malformed"} {
		t.Run(name, func(t *testing.T) {
			objects := make([]Object, 20)
			for i := range objects {
				objects[i] = traceObject(fmt.Sprint(i), "team")
				if i+1 < len(objects) {
					objects[i].Owners = []metav1.OwnerReference{traceOwner(fmt.Sprint(i + 1))}
				}
			}
			var kind, cause error
			wantReads := 18
			switch name {
			case "missing":
				kind = ErrMissing
				cause = apierrors.NewNotFound(schema.GroupResource{Resource: "workloads"}, "18")
			case "forbidden":
				cause = apierrors.NewForbidden(schema.GroupResource{Resource: "workloads"}, "18", errors.New("denied"))
			case "canceled":
				cause = context.Canceled
			case "malformed":
				kind = ErrInvalidReference
				objects[17].Owners[0].APIVersion = "invalid/version/extra"
				wantReads = 17
			}
			var failures [2]*Error
			for run := range 2 {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				reads := 0
				lookup := func(_ context.Context, ref Ref) (Object, error) {
					reads++
					if reads == 18 {
						if name == "canceled" {
							cancel()
						} else if cause != nil {
							return Object{}, cause
						}
					}
					return objects[reads], nil
				}
				var err error
				if run == 0 {
					var path []Object
					path, err = Trace(ctx, objects[0], lookup, 32)
					if !reflect.DeepEqual(path, objects[:18]) {
						t.Fatalf("verified object prefix = %+v", path)
					}
					path[0].Ref.Name = "caller-mutated"
				} else {
					var root Ref
					root, err = TraceRoot(ctx, objects[0], lookup, 32)
					if root != (Ref{}) {
						t.Fatalf("failed root = %+v", root)
					}
				}
				if !errors.As(err, &failures[run]) || failures[run].Kind != kind || len(failures[run].Path) != 18 || reads != wantReads {
					t.Fatalf("error=%+v, reads=%d", err, reads)
				}
				if cause != nil && !errors.Is(err, cause) {
					t.Fatalf("lost cause %v: %v", cause, err)
				}
				for i := range failures[run].Path {
					if failures[run].Path[i] != objects[i].Ref {
						t.Fatalf("verified reference prefix[%d] = %+v", i, failures[run].Path[i])
					}
				}
			}
			if !reflect.DeepEqual(failures[0], failures[1]) {
				t.Fatalf("trace/root errors differ: %+v/%+v", failures[0], failures[1])
			}
			preserved := append([]Ref(nil), failures[1].Path...)
			objects[17].Owners[0].APIVersion = "example.test/v1"
			index := 0
			_, err := Trace(context.Background(), objects[0], func(context.Context, Ref) (Object, error) {
				index++
				return objects[index], nil
			}, 32)
			if err != nil || !reflect.DeepEqual(failures[1].Path, preserved) {
				t.Fatalf("retained failure changed after a later traversal: %+v, error=%v", failures[1], err)
			}
		})
	}
}
