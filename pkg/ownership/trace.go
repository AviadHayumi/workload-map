// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ownership

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	var scratch [16]Object
	path, err := walk(ctx, start, lookup, maxDepth, scratch[:0])
	// Copy only the observed prefix, not storage reserved for an arbitrary depth.
	return append([]Object(nil), path...), err
}

// TraceRoot follows controller ownership and returns the terminal object's reference.
// Validation and depth limits match Trace. On failure, the returned reference is
// empty and the typed Error contains the same verified prefix as Trace.
func TraceRoot(ctx context.Context, start Object, lookup Lookup, maxDepth int) (Ref, error) {
	if len(start.Owners) == 0 {
		var scratch [1]Object
		if _, err := walk(ctx, start, lookup, maxDepth, scratch[:0]); err != nil {
			return Ref{}, err
		}
		return start.Ref, nil
	}
	return traceOwnedRoot(ctx, start, lookup, maxDepth)
}

// Keep the larger path scratch out of terminal-object calls.
func traceOwnedRoot(ctx context.Context, start Object, lookup Lookup, maxDepth int) (Ref, error) {
	var scratch [16]Object
	path, err := walk(ctx, start, lookup, maxDepth, scratch[:0])
	if err != nil {
		return Ref{}, err
	}
	return path[len(path)-1].Ref, nil
}

func walk(ctx context.Context, start Object, lookup Lookup, maxDepth int, scratch []Object) ([]Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, walkError(nil, nil, start.Ref, Ref{}, err)
	}
	if maxDepth < 0 {
		return nil, walkError(ErrInvalidReference, nil, start.Ref, Ref{}, errors.New("maxDepth must not be negative"))
	}
	if err := validateObject(start); err != nil {
		return nil, walkError(err, nil, start.Ref, start.Ref, nil)
	}
	path := append(scratch[:0], start)
	var seen map[types.UID]struct{}
	current := start
	for depth := 0; ; depth++ {
		if err := ctx.Err(); err != nil {
			return path, walkError(nil, path, current.Ref, Ref{}, err)
		}
		ownerIndex, err := selectController(current.Owners, true)
		if err != nil {
			return path, walkError(err, path, current.Ref, Ref{}, nil)
		}
		if ownerIndex < 0 {
			return path, nil
		}
		owner := current.Owners[ownerIndex]
		gv, parseErr := schema.ParseGroupVersion(owner.APIVersion)
		next := Ref{Namespace: current.Ref.Namespace, UID: owner.UID, GVK: gv.WithKind(owner.Kind), Name: owner.Name}
		if parseErr != nil || gv.Version == "" || validateRef(next) != nil {
			return path, walkError(ErrInvalidReference, path, next, Ref{}, parseErr)
		}
		if depth >= maxDepth {
			return path, walkError(ErrDepthExceeded, path, next, Ref{}, nil)
		}
		// Common ownership chains fit in the path scratch. Longer chains switch
		// to a UID set so cycle detection remains linear in total chain length.
		if seen == nil && len(path) > 16 {
			seen = make(map[types.UID]struct{}, len(path))
			for i := range path {
				seen[path[i].Ref.UID] = struct{}{}
			}
		}
		if seen != nil {
			if _, exists := seen[next.UID]; exists {
				return path, walkError(ErrCycle, path, next, Ref{}, nil)
			}
		} else {
			for i := range path {
				if path[i].Ref.UID == next.UID {
					return path, walkError(ErrCycle, path, next, Ref{}, nil)
				}
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
		if seen != nil {
			seen[actual.Ref.UID] = struct{}{}
		}
		path = append(path, actual)
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
	if strings.Contains(ref.GVK.Group, "/") || strings.Contains(ref.GVK.Version, "/") {
		return ErrInvalidReference
	}
	return nil
}

func selectController(ownerRefs []metav1.OwnerReference, strict bool) (int, error) {
	controller := -1
	for index := range ownerRefs {
		ref := ownerRefs[index]
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		if controller >= 0 {
			return -1, ErrMultipleControllers
		}
		controller = index
		if !strict {
			break
		}
	}
	return controller, nil
}
