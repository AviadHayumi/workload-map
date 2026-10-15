// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ownership

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Object contains the identity, scope, and ownership metadata used by Trace.
type Object struct {
	Ref        Ref
	Owners     []metav1.OwnerReference
	Namespaced bool
}

// Lookup reads an owner in the dependent object's namespace. Implementations
// resolve the requested GVK's scope and return the owner's actual namespace.
type Lookup func(context.Context, Ref) (Object, error)

// Error identifies a failed edge and preserves its underlying lookup error.
// Path contains the verified objects before the failed edge, starting at the seed.
type Error struct {
	Kind     error
	Expected Ref
	Actual   Ref
	Path     []Ref
	Cause    error
}

type walkPolicy struct {
	strict       bool
	beforeLookup func(Ref) bool
}

var (
	ErrMissing             = errors.New("owner is missing")
	ErrUIDMismatch         = errors.New("owner UID does not match")
	ErrCycle               = errors.New("ownership cycle")
	ErrDepthExceeded       = errors.New("ownership depth exceeded")
	ErrScope               = errors.New("invalid ownership scope")
	ErrMultipleControllers = errors.New("multiple controller owners")
	ErrInvalidReference    = errors.New("invalid owner reference")
)

func (e *Error) Error() string {
	message := "ownership traversal failed"
	if e.Kind != nil {
		message = e.Kind.Error()
	}
	if e.Expected.Name != "" {
		message = fmt.Sprintf("%s: %s %s/%s (UID %s)", message, e.Expected.GVK.String(), e.Expected.Namespace, e.Expected.Name, e.Expected.UID)
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *Error) Unwrap() []error {
	var causes []error
	if e.Kind != nil {
		causes = append(causes, e.Kind)
	}
	if e.Cause != nil {
		causes = append(causes, e.Cause)
	}
	return causes
}

// Trace follows controller ownership from the seed to the terminal object.
// maxDepth limits owner hops, excluding the seed. A zero limit permits only a
// terminal seed. On failure, Trace returns the verified prefix and a typed Error.
// Different served API versions may identify the same group-kind and UID.
func Trace(ctx context.Context, start Object, lookup Lookup, maxDepth int) ([]Object, error) {
	return walk(ctx, start, lookup, maxDepth, walkPolicy{strict: true})
}

func walk(ctx context.Context, start Object, lookup Lookup, maxDepth int, policy walkPolicy) ([]Object, error) {
	var path []Object
	var seen map[types.UID]struct{}
	if policy.strict {
		if err := ctx.Err(); err != nil {
			return nil, walkError(nil, nil, start.Ref, Ref{}, err)
		}
		if maxDepth < 0 {
			return nil, walkError(ErrInvalidReference, nil, start.Ref, Ref{}, errors.New("maxDepth must not be negative"))
		}
		if err := validateObject(start); err != nil {
			return nil, walkError(err, nil, start.Ref, start.Ref, nil)
		}
		path = []Object{start}
		seen = map[types.UID]struct{}{start.Ref.UID: {}}
	}
	current := start
	for depth := 0; ; depth++ {
		if err := ctx.Err(); err != nil {
			return path, walkError(nil, path, current.Ref, Ref{}, err)
		}
		owner, err := selectController(current.Owners, policy.strict)
		if err != nil {
			return path, walkError(err, path, current.Ref, Ref{}, nil)
		}
		if owner == nil {
			return path, nil
		}
		next := Ref{Namespace: current.Ref.Namespace, UID: owner.UID, GVK: refGVK(owner), Name: owner.Name}
		if policy.strict {
			gv, parseErr := schema.ParseGroupVersion(owner.APIVersion)
			if parseErr != nil || gv.Version == "" || validateRef(next) != nil {
				return path, walkError(ErrInvalidReference, path, next, Ref{}, parseErr)
			}
		}
		if policy.beforeLookup != nil && policy.beforeLookup(next) {
			return path, nil
		}
		if depth >= maxDepth {
			return path, walkError(ErrDepthExceeded, path, next, Ref{}, nil)
		}
		if policy.strict {
			if _, exists := seen[next.UID]; exists {
				return path, walkError(ErrCycle, path, next, Ref{}, nil)
			}
		}
		if lookup == nil {
			return path, walkError(ErrInvalidReference, path, next, Ref{}, errors.New("lookup is required for an owner edge"))
		}
		actual, err := lookup(ctx, next)
		if err != nil {
			var kind error
			if apierrors.IsNotFound(err) {
				kind = ErrMissing
			}
			return path, walkError(kind, path, next, Ref{}, err)
		}
		if err := ctx.Err(); err != nil {
			return path, walkError(nil, path, next, Ref{}, err)
		}
		if policy.strict {
			if actual.Ref.UID != next.UID {
				return path, walkError(ErrUIDMismatch, path, next, actual.Ref, nil)
			}
			if err := validateObject(actual); err != nil {
				return path, walkError(err, path, next, actual.Ref, nil)
			}
			if actual.Ref.Name != next.Name || actual.Ref.GVK.GroupKind() != next.GVK.GroupKind() {
				return path, walkError(ErrInvalidReference, path, next, actual.Ref, nil)
			}
			if actual.Namespaced && (!current.Namespaced || actual.Ref.Namespace != current.Ref.Namespace) {
				return path, walkError(ErrScope, path, next, actual.Ref, nil)
			}
			seen[actual.Ref.UID] = struct{}{}
			path = append(path, actual)
		}
		current = actual
	}
}

func walkError(kind error, path []Object, expected, actual Ref, cause error) *Error {
	refs := make([]Ref, len(path))
	for i := range path {
		refs[i] = path[i].Ref
	}
	return &Error{Kind: kind, Expected: expected, Actual: actual, Path: refs, Cause: cause}
}

func validateObject(object Object) error {
	if err := validateRef(object.Ref); err != nil {
		return err
	}
	if object.Namespaced != (object.Ref.Namespace != "") {
		return ErrScope
	}
	return nil
}

func validateRef(ref Ref) error {
	if ref.UID == "" || ref.Name == "" || ref.GVK.Kind == "" || ref.GVK.Version == "" {
		return ErrInvalidReference
	}
	gv, err := schema.ParseGroupVersion(ref.GVK.GroupVersion().String())
	if err != nil || gv != ref.GVK.GroupVersion() {
		return ErrInvalidReference
	}
	return nil
}

func selectController(ownerRefs []metav1.OwnerReference, strict bool) (*metav1.OwnerReference, error) {
	var controller *metav1.OwnerReference
	for index := range ownerRefs {
		ref := ownerRefs[index]
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		if controller != nil {
			return nil, ErrMultipleControllers
		}
		controller = &ref
		if !strict {
			break
		}
	}
	return controller, nil
}
