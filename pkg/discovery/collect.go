// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type objectKey struct {
	gvk       schema.GroupVersionKind
	namespace string
	name      string
}

type collection struct {
	reader     Reader
	mappings   map[schema.GroupVersionKind]*meta.RESTMapping
	objects    map[objectKey]*unstructured.Unstructured
	identities map[types.UID]*unstructured.Unstructured
	roots      map[types.UID]types.UID
	pods       map[objectKey]*corev1.Pod
	podUIDs    map[types.UID]*corev1.Pod
	metadata   map[*unstructured.Unstructured]ownership.Object
}

func newCollection(reader Reader) *collection {
	return &collection{reader: reader, mappings: make(map[schema.GroupVersionKind]*meta.RESTMapping), objects: make(map[objectKey]*unstructured.Unstructured), identities: make(map[types.UID]*unstructured.Unstructured), roots: make(map[types.UID]types.UID), pods: make(map[objectKey]*corev1.Pod), podUIDs: make(map[types.UID]*corev1.Pod), metadata: make(map[*unstructured.Unstructured]ownership.Object)}
}

func (c *collection) mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mapping := c.mappings[gvk]; mapping != nil {
		return mapping, nil
	}
	mapping, err := c.reader.Mapping(ctx, gvk)
	if err != nil {
		return nil, fmt.Errorf("map %s: %w", gvk, err)
	}
	mapping, err = copyMapping(mapping)
	if err != nil {
		return nil, err
	}
	if mapping.GroupVersionKind != gvk {
		return nil, fmt.Errorf("%w: mapping for %s returned %s", ErrInvalidInput, gvk, mapping.GroupVersionKind)
	}
	c.mappings[gvk] = mapping
	return mapping, nil
}

func (c *collection) get(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	mapping, err := c.mapping(ctx, ref.GVK)
	if err != nil {
		return nil, err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		ref.Namespace = ""
	} else if ref.Namespace == "" {
		return nil, fmt.Errorf("%w: namespaced lookup has no dependent namespace", ownership.ErrScope)
	}
	key := objectKey{ref.GVK, ref.Namespace, ref.Name}
	object := c.objects[key]
	if object == nil {
		if pod := c.pods[key]; pod != nil {
			object, err = podObject(pod)
		} else {
			object, err = c.reader.Get(ctx, ref)
			if object != nil {
				object = object.DeepCopy()
			}
		}
		if err != nil {
			return nil, err
		}
		if object == nil {
			return nil, fmt.Errorf("%w: nil result for %s", ErrInvalidInput, ref.Name)
		}
		if object.GroupVersionKind() != ref.GVK || object.GetName() != ref.Name || object.GetNamespace() != ref.Namespace {
			return nil, fmt.Errorf("%w: lookup coordinates disagree for %s", ErrStaleInput, ref.Name)
		}
		if err := c.remember(object); err != nil {
			return nil, err
		}
	}
	if object.GetUID() != ref.UID {
		return nil, &ownership.Error{Kind: ownership.ErrUIDMismatch, Expected: ref, Actual: reference(object)}
	}
	return object, nil
}

func (c *collection) lookup(ctx context.Context, ref ObjectRef) (ownership.Object, error) {
	object, err := c.get(ctx, ref)
	if err != nil {
		return ownership.Object{}, err
	}
	return c.ownershipMetadata(object)
}

func (c *collection) trace(ctx context.Context, object *unstructured.Unstructured, depth int) ([]ownership.Object, error) {
	if _, err := c.mapping(ctx, object.GroupVersionKind()); err != nil {
		return nil, err
	}
	start, err := c.ownershipMetadata(object)
	if err != nil {
		return nil, err
	}
	return ownership.Trace(ctx, start, c.lookup, depth)
}

// Collection objects are detached and immutable. Pointer keys keep metadata
// separate for each served version and each observed object body.
func (c *collection) ownershipMetadata(object *unstructured.Unstructured) (ownership.Object, error) {
	if metadata, ok := c.metadata[object]; ok {
		return metadata, nil
	}
	metadata, err := ownershipObject(object, c.mappings[object.GroupVersionKind()])
	if err != nil {
		return ownership.Object{}, err
	}
	c.metadata[object] = metadata
	return metadata, nil
}

// Collect captures a complete Pod list and its owner closure. extraRoots adds
// explicit empty workloads. It never discovers arbitrary zero-Pod roots by
// listing every resource kind. No partial dataset is returned on read failure.
func Collect(ctx context.Context, reader Reader, namespace string, extraRoots ...ObjectRef) (Dataset, error) {
	if isNil(reader) {
		return Dataset{}, ErrNoClient
	}
	return collect(ctx, newCollection(reader), namespace, 32, extraRoots...)
}

func collect(ctx context.Context, c *collection, namespace string, depth int, extras ...ObjectRef) (Dataset, error) {
	if err := ctx.Err(); err != nil {
		return Dataset{}, err
	}
	list, err := c.reader.ListPods(ctx, namespace)
	if err != nil {
		return Dataset{}, fmt.Errorf("list Pods in %q: %w", namespace, err)
	}
	if list == nil || list.Continue != "" {
		return Dataset{}, fmt.Errorf("%w: reader returned a partial Pod list", ErrIncompleteOwnership)
	}
	if _, err := c.mapping(ctx, podGVK); err != nil {
		return Dataset{}, err
	}
	dataset := Dataset{Namespace: namespace, PodResourceVersion: list.ResourceVersion}
	seen := make(map[types.UID]bool, len(list.Items))
	seenNames := make(map[objectKey]bool, len(list.Items))
	for index := range list.Items {
		if err := ctx.Err(); err != nil {
			return Dataset{}, err
		}
		pod := list.Items[index].DeepCopy()
		if namespace != "" && pod.Namespace != namespace {
			return Dataset{}, fmt.Errorf("%w: Pod outside requested namespace", ErrInsufficientScope)
		}
		if _, err := c.podIdentity(ctx, pod); err != nil {
			return Dataset{}, err
		}
		if seen[pod.UID] {
			return Dataset{}, fmt.Errorf("%w: duplicate Pod UID %s", ErrInvalidInput, pod.UID)
		}
		seen[pod.UID] = true
		key := objectKey{podGVK, pod.Namespace, pod.Name}
		if seenNames[key] {
			return Dataset{}, fmt.Errorf("%w: duplicate Pod coordinates", ErrInvalidInput)
		}
		seenNames[key] = true
		if previous := c.objects[key]; previous != nil && (previous.GetUID() != pod.UID || previous.GetResourceVersion() != pod.ResourceVersion) {
			return Dataset{}, fmt.Errorf("%w: Pod changed during collection", ErrStaleInput)
		}
		c.pods[key] = pod
		c.podUIDs[pod.UID] = pod
		dataset.Pods = append(dataset.Pods, pod)
	}
	// Index the entire Pod list first because a Pod can itself control another Pod.
	for _, pod := range dataset.Pods {
		path, err := c.tracePod(ctx, pod, depth)
		if err != nil {
			return Dataset{}, fmt.Errorf("%w: Pod %s/%s: %w", ErrIncompleteOwnership, pod.Namespace, pod.Name, err)
		}
		c.roots[pod.UID] = path[len(path)-1].Ref.UID
	}
	for _, ref := range extras {
		object, err := c.get(ctx, ref)
		if err != nil {
			return Dataset{}, err
		}
		if _, err := c.trace(ctx, object, depth); err != nil {
			return Dataset{}, fmt.Errorf("%w: %w", ErrIncompleteOwnership, err)
		}
	}
	for _, object := range c.objects {
		if object.GroupVersionKind() == podGVK && !seen[object.GetUID()] {
			return Dataset{}, fmt.Errorf("%w: owner Pod was absent from complete Pod list", ErrStaleInput)
		}
		if object.GroupVersionKind() != podGVK {
			dataset.Objects = append(dataset.Objects, object)
		}
	}
	sort.Slice(dataset.Objects, func(i, j int) bool { return string(dataset.Objects[i].GetUID()) < string(dataset.Objects[j].GetUID()) })
	sort.Slice(dataset.Pods, func(i, j int) bool { return podLess(dataset.Pods[i], dataset.Pods[j]) })
	dataset.Mappings = c.mappings
	return dataset, nil
}

func podLess(a, b *corev1.Pod) bool {
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.UID < b.UID
}

// remember rejects conflicting versions of one lifetime within an observation.
func (c *collection) remember(object *unstructured.Unstructured) error {
	ref := reference(object)
	if pod := c.podUIDs[ref.UID]; pod != nil {
		if ref != podReference(pod) || object.GetResourceVersion() != pod.ResourceVersion || !sameOwnerReferences(object.GetOwnerReferences(), pod.OwnerReferences) {
			return fmt.Errorf("%w: Pod identity disagrees with owner observation", ErrStaleInput)
		}
	}
	if prior := c.identities[ref.UID]; prior != nil {
		previous := reference(prior)
		if previous.GVK.GroupKind() != ref.GVK.GroupKind() || previous.Namespace != ref.Namespace || previous.Name != ref.Name || prior.GetResourceVersion() != object.GetResourceVersion() || !sameOwnerReferences(prior.GetOwnerReferences(), object.GetOwnerReferences()) {
			return fmt.Errorf("%w: inconsistent observed aliases for UID %s", ErrStaleInput, ref.UID)
		}
	}
	c.identities[ref.UID] = object
	c.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}] = object
	return nil
}

func podReference(pod *corev1.Pod) ObjectRef {
	return ObjectRef{GVK: podGVK, Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID}
}

func (c *collection) podIdentity(ctx context.Context, pod *corev1.Pod) (ownership.Object, error) {
	if pod == nil {
		return ownership.Object{}, fmt.Errorf("%w: nil Pod", ErrInvalidInput)
	}
	ref := podReference(pod)
	if previous := c.identities[pod.UID]; previous != nil && (reference(previous) != ref || previous.GetResourceVersion() != pod.ResourceVersion || !sameOwnerReferences(previous.GetOwnerReferences(), pod.OwnerReferences)) {
		return ownership.Object{}, fmt.Errorf("%w: Pod conflicts with acquired owner identity", ErrStaleInput)
	}
	if err := validateRef(ref); err != nil {
		return ownership.Object{}, err
	}
	mapping, err := c.mapping(ctx, podGVK)
	if err != nil {
		return ownership.Object{}, err
	}
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace || pod.Namespace == "" {
		return ownership.Object{}, fmt.Errorf("%w: Pod requires namespace scope", ownership.ErrScope)
	}
	return ownership.Object{Ref: ref, Owners: pod.OwnerReferences, Namespaced: true}, nil
}

func (c *collection) tracePod(ctx context.Context, pod *corev1.Pod, depth int) ([]ownership.Object, error) {
	start, err := c.podIdentity(ctx, pod)
	if err != nil {
		return nil, err
	}
	return ownership.Trace(ctx, start, c.lookup, depth)
}
