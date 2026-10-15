// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ownership

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSelectControllerIndex(t *testing.T) {
	owner := traceOwner("owner")
	other := traceOwner("other")
	implicit := traceOwner("implicit")
	implicit.Controller = nil
	passive := traceOwner("passive")
	*passive.Controller = false
	for _, tt := range []struct {
		name   string
		refs   []metav1.OwnerReference
		strict bool
		want   int
		err    error
	}{
		{name: "no owners", strict: true, want: -1},
		{name: "no controllers", refs: []metav1.OwnerReference{implicit, passive}, strict: true, want: -1},
		{name: "strict skips passive owners", refs: []metav1.OwnerReference{implicit, passive, owner, passive}, strict: true, want: 2},
		{name: "strict rejects later controller", refs: []metav1.OwnerReference{owner, passive, other}, strict: true, want: -1, err: ErrMultipleControllers},
		{name: "strict rejects duplicate controller", refs: []metav1.OwnerReference{owner, owner}, strict: true, want: -1, err: ErrMultipleControllers},
		{name: "legacy first controller", refs: []metav1.OwnerReference{implicit, passive, owner, other}, want: 2},
		{name: "legacy no controller", refs: []metav1.OwnerReference{implicit, passive}, want: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			index, err := selectController(tt.refs, tt.strict)
			if index != tt.want || err != tt.err {
				t.Fatalf("selection = %d, %v; want %d, %v", index, err, tt.want, tt.err)
			}
		})
	}
}

func TestControllerRefRetainsCopyAfterCallerMutation(t *testing.T) {
	original := traceOwner("owner")
	refs := []metav1.OwnerReference{original}
	selected := controllerRef(refs)
	index := New()
	index.UpsertObject("child", refs)
	refs[0] = traceOwner("replacement")
	refs[0].APIVersion = "other.test/v2"
	refs[0].Kind = "Different"
	if selected == nil || !reflect.DeepEqual(*selected, original) {
		t.Fatalf("selected owner changed with caller slice: %+v", selected)
	}
	retained := index.nodes["child"].owner
	if retained == nil || !reflect.DeepEqual(*retained, original) {
		t.Fatalf("retained index owner changed with caller slice: %+v", retained)
	}
	if _, ok := index.children[original.UID]["child"]; !ok || len(index.children[refs[0].UID]) != 0 {
		t.Fatal("recorded ownership edge disagrees with reverse index")
	}
}

func TestTraceControllerFailurePrecedesReferenceAndDepthValidation(t *testing.T) {
	invalid := traceOwner("owner")
	invalid.APIVersion = "invalid/version/extra"
	start := traceObject("seed", "team", invalid, traceOwner("other"))
	path, err := Trace(context.Background(), start, func(context.Context, Ref) (Object, error) {
		t.Fatal("multiple controllers reached lookup")
		return Object{}, nil
	}, 0)
	var failure *Error
	if !errors.Is(err, ErrMultipleControllers) || !errors.As(err, &failure) {
		t.Fatalf("error = %v, want multiple controllers", err)
	}
	if !reflect.DeepEqual(path, []Object{start}) || !reflect.DeepEqual(failure.Path, []Ref{start.Ref}) || failure.Expected != start.Ref || failure.Actual != (Ref{}) || failure.Cause != nil {
		t.Fatalf("selection diagnostics: path=%+v, error=%+v", path, failure)
	}
}
