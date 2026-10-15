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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func traceObject(name, namespace string, owners ...metav1.OwnerReference) Object {
	return Object{
		Ref:        Ref{UID: types.UID(name), Name: name, Namespace: namespace, GVK: schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Workload"}},
		Owners:     owners,
		Namespaced: namespace != "",
	}
}

func traceOwner(name string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "example.test/v1", Kind: "Workload", Name: name, UID: types.UID(name), Controller: ptr.To(true)}
}

func TestTraceReturnsVerifiedPathAndResolvesScopeAtEveryHop(t *testing.T) {
	start := traceObject("pod", "team", traceOwner("child"))
	child := traceObject("child", "team", traceOwner("root"))
	root := traceObject("root", "", traceOwner("outer"))
	outer := traceObject("outer", "")
	// A served version alias still denotes the same object.
	child.Ref.GVK.Version = "v2"
	objects := map[string]Object{"child": child, "root": root, "outer": outer}
	var requests []Ref
	lookup := func(_ context.Context, ref Ref) (Object, error) {
		requests = append(requests, ref)
		return objects[ref.Name], nil
	}
	path, err := Trace(context.Background(), start, lookup, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(path, []Object{start, child, root, outer}) {
		t.Fatalf("path = %+v", path)
	}
	if len(requests) != 3 || requests[0].Namespace != "team" || requests[1].Namespace != "team" || requests[2].Namespace != "" {
		t.Fatalf("lookup must receive each dependent namespace: %+v", requests)
	}
}

func TestTraceRejectsIdentityAndScopeFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Object, *Object)
		want   error
	}{
		{"recreated owner", func(_, owner *Object) { owner.Ref.UID = "replacement" }, ErrUIDMismatch},
		{"different name", func(_, owner *Object) { owner.Ref.Name = "different" }, ErrInvalidReference},
		{"different kind", func(_, owner *Object) { owner.Ref.GVK.Kind = "Different" }, ErrInvalidReference},
		{"different group", func(_, owner *Object) { owner.Ref.GVK.Group = "different.test" }, ErrInvalidReference},
		{"cross namespace", func(_, owner *Object) { owner.Ref.Namespace = "other" }, ErrScope},
		{"cluster dependent", func(start, _ *Object) { start.Ref.Namespace = ""; start.Namespaced = false }, ErrScope},
		{"cluster object has namespace", func(_, owner *Object) { owner.Namespaced = false }, ErrScope},
		{"namespaced object lacks namespace", func(_, owner *Object) { owner.Ref.Namespace = "" }, ErrScope},
		{"missing actual version", func(_, owner *Object) { owner.Ref.GVK.Version = "" }, ErrInvalidReference},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := traceObject("pod", "team", traceOwner("owner"))
			owner := traceObject("owner", "team")
			tt.mutate(&start, &owner)
			path, err := Trace(context.Background(), start, func(context.Context, Ref) (Object, error) { return owner, nil }, 32)
			var failure *Error
			if !errors.Is(err, tt.want) || !errors.As(err, &failure) {
				t.Fatalf("error = %v, want typed %v", err, tt.want)
			}
			if len(path) != 1 || len(failure.Path) != 1 || failure.Path[0] != start.Ref || failure.Actual != owner.Ref || failure.Expected.UID != "owner" {
				t.Fatalf("incorrect verified prefix/diagnostics: path=%+v error=%+v", path, failure)
			}
		})
	}
}

func TestTraceRejectsInvalidReferencesBeforeLookup(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*metav1.OwnerReference)
	}{
		{"missing UID", func(ref *metav1.OwnerReference) { ref.UID = "" }},
		{"missing name", func(ref *metav1.OwnerReference) { ref.Name = "" }},
		{"missing kind", func(ref *metav1.OwnerReference) { ref.Kind = "" }},
		{"missing version", func(ref *metav1.OwnerReference) { ref.APIVersion = "" }},
		{"empty version", func(ref *metav1.OwnerReference) { ref.APIVersion = "example.test/" }},
		{"malformed version", func(ref *metav1.OwnerReference) { ref.APIVersion = "example.test/v1/invalid" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := traceOwner("owner")
			tt.mutate(&ref)
			_, err := Trace(context.Background(), traceObject("pod", "team", ref), func(context.Context, Ref) (Object, error) {
				t.Fatal("invalid reference reached lookup")
				return Object{}, nil
			}, 32)
			if !errors.Is(err, ErrInvalidReference) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for _, object := range []Object{{}, {Ref: Ref{UID: "seed", Name: "seed", GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}}, Namespaced: true}} {
		path, err := Trace(context.Background(), object, nil, 32)
		if err == nil || len(path) != 0 {
			t.Fatalf("invalid seed accepted: %+v, %v", path, err)
		}
	}
}

func TestTraceControllerSelection(t *testing.T) {
	passive := traceOwner("passive")
	passive.Controller = ptr.To(false)
	implicit := traceOwner("implicit")
	implicit.Controller = nil
	owner := traceOwner("owner")
	for _, refs := range [][]metav1.OwnerReference{nil, {passive}, {implicit, passive}} {
		path, err := Trace(context.Background(), traceObject("pod", "team", refs...), nil, 0)
		if err != nil || len(path) != 1 {
			t.Fatalf("noncontroller reference followed: %+v, %v", path, err)
		}
	}
	path, err := Trace(context.Background(), traceObject("pod", "team", passive, implicit, owner), func(_ context.Context, ref Ref) (Object, error) {
		if ref.UID != owner.UID {
			t.Fatalf("followed noncontroller: %+v", ref)
		}
		return traceObject("owner", "team"), nil
	}, 1)
	if err != nil || len(path) != 2 {
		t.Fatalf("single controller failed: %+v, %v", path, err)
	}
	for _, refs := range [][]metav1.OwnerReference{{owner, traceOwner("other")}, {owner, owner}} {
		_, err := Trace(context.Background(), traceObject("pod", "team", refs...), func(context.Context, Ref) (Object, error) {
			t.Fatal("multiple controllers reached lookup")
			return Object{}, nil
		}, 32)
		if !errors.Is(err, ErrMultipleControllers) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestTraceCycleAndDepth(t *testing.T) {
	start := traceObject("a", "team", traceOwner("b"))
	reads := 0
	lookup := func(context.Context, Ref) (Object, error) {
		reads++
		return traceObject("b", "team", traceOwner("a")), nil
	}
	path, err := Trace(context.Background(), start, lookup, 32)
	if !errors.Is(err, ErrCycle) || len(path) != 2 || reads != 1 {
		t.Fatalf("cycle = %+v, %v, %d reads", path, err, reads)
	}
	reads = 0
	path, err = Trace(context.Background(), start, lookup, 0)
	if !errors.Is(err, ErrDepthExceeded) || len(path) != 1 || reads != 0 {
		t.Fatalf("zero depth = %+v, %v, %d reads", path, err, reads)
	}
	_, err = Trace(context.Background(), start, lookup, -1)
	if !errors.Is(err, ErrInvalidReference) || reads != 0 {
		t.Fatalf("negative depth = %v, %d reads", err, reads)
	}
	lookup = func(_ context.Context, ref Ref) (Object, error) {
		return traceObject(ref.Name, "team", traceOwner("next")), nil
	}
	path, err = Trace(context.Background(), start, lookup, 1)
	if !errors.Is(err, ErrDepthExceeded) || len(path) != 2 {
		t.Fatalf("one-hop depth = %+v, %v", path, err)
	}
}

func TestTracePreservesLookupCauses(t *testing.T) {
	missing := apierrors.NewNotFound(schema.GroupResource{Group: "example.test", Resource: "workloads"}, "owner")
	for _, cause := range []error{missing, apierrors.NewForbidden(schema.GroupResource{Resource: "workloads"}, "owner", errors.New("denied")), context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			path, err := Trace(context.Background(), traceObject("pod", "team", traceOwner("owner")), func(context.Context, Ref) (Object, error) {
				return Object{}, fmt.Errorf("read: %w", cause)
			}, 32)
			var failure *Error
			if !errors.Is(err, cause) || !errors.As(err, &failure) || len(path) != 1 {
				t.Fatalf("lost lookup cause: %+v, %v", path, err)
			}
			if cause == missing && (!errors.Is(err, ErrMissing) || !apierrors.IsNotFound(err)) {
				t.Fatalf("missing classification lost: %v", err)
			}
			var status *apierrors.StatusError
			if errors.As(cause, &status) && !errors.As(err, &status) {
				t.Fatalf("lost API status error: %v", err)
			}
			if failure.Expected.Name != "owner" || failure.Expected.Namespace != "team" || len(failure.Path) != 1 || failure.Error() == "" {
				t.Fatalf("missing diagnostics: %+v", failure)
			}
		})
	}
}

func TestTraceCancellationBeforeAndDuringLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Trace(ctx, traceObject("pod", "team", traceOwner("owner")), func(context.Context, Ref) (Object, error) {
		t.Fatal("canceled walk reached lookup")
		return Object{}, nil
	}, 32)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	path, err := Trace(ctx, traceObject("pod", "team", traceOwner("owner")), func(actual context.Context, _ Ref) (Object, error) {
		if actual != ctx {
			t.Fatal("context not forwarded")
		}
		cancel()
		return traceObject("owner", "team"), nil
	}, 32)
	if !errors.Is(err, context.Canceled) || len(path) != 1 {
		t.Fatalf("cancellation was ignored after lookup: %+v, %v", path, err)
	}
}
