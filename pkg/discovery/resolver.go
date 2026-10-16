// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

// Resolver acquires complete observed workload membership using an injected
// client. It owns no goroutines or client lifecycle. Results are immutable
// observations, not continuously refreshed handles.
type Resolver struct {
	reader   Reader
	plans    map[schema.GroupVersionKind]*attribution.Plan
	maxDepth int
}

// Options controls definition selection and the owner-hop limit. Definitions
// override the built-in catalog by exact GVK. MaxDepth zero selects 32 hops.
type Options struct {
	Definitions []*v1alpha1.Karta
	MaxDepth    int
}

// New copies and validates definitions without accessing Kubernetes. A nil
// reader is allowed for construction; acquisition then returns ErrNoClient.
func New(reader Reader, options Options) (*Resolver, error) {
	if options.MaxDepth < 0 {
		return nil, fmt.Errorf("%w: negative MaxDepth", ErrInvalidInput)
	}
	if options.MaxDepth == 0 {
		options.MaxDepth = 32
	}
	if isNil(reader) {
		reader = nil
	}
	definitions := make(map[schema.GroupVersionKind]*v1alpha1.Karta)
	for _, definition := range catalog.List() {
		definitions[catalog.RootKey(definition)] = definition
	}
	seen := make(map[schema.GroupVersionKind]bool)
	for _, definition := range options.Definitions {
		if definition == nil {
			return nil, fmt.Errorf("%w: nil definition", ErrInvalidInput)
		}
		key := catalog.RootKey(definition)
		if key.Version == "" || key.Kind == "" {
			return nil, fmt.Errorf("%w: definition requires root GVK", ErrInvalidInput)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate definition for %s", ErrInvalidInput, key)
		}
		seen[key] = true
		definitions[key] = definition
	}
	r := &Resolver{reader: reader, maxDepth: options.MaxDepth, plans: make(map[schema.GroupVersionKind]*attribution.Plan)}
	for key, definition := range definitions {
		plan, err := attribution.Compile(definition)
		if err != nil {
			return nil, fmt.Errorf("definition for %s: %w", key, err)
		}
		r.plans[key] = plan
	}
	return r, nil
}

// WorkloadForPod returns the terminal controller-owned workload and all its
// descendant Pods, including completed, terminating and unscheduled Pods.
func (r *Resolver) WorkloadForPod(ctx context.Context, pod ObjectRef) (*Workload, error) {
	if r == nil || r.reader == nil {
		return nil, ErrNoClient
	}
	if pod.GVK != podGVK {
		return nil, fmt.Errorf("%w: WorkloadForPod requires v1/Pod", ErrInvalidInput)
	}
	return r.acquire(ctx, pod, false)
}

// Workload observes an explicit terminal root, including a zero-Pod workload.
// An intermediate owner returns ErrNotWorkloadRoot.
func (r *Resolver) Workload(ctx context.Context, root ObjectRef) (*Workload, error) {
	return r.acquire(ctx, root, true)
}

// Snapshot bridges an existing offline factory to live membership. Its object
// UID/resourceVersion and definition must match the acquired observation.
func (r *Resolver) Snapshot(ctx context.Context, factory *resource.ComponentFactory) (*Workload, error) {
	if r == nil || r.reader == nil {
		return nil, ErrNoClient
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if factory == nil || factory.GetKarta() == nil {
		return nil, fmt.Errorf("%w: nil factory or definition", ErrInvalidInput)
	}
	object, err := factory.GetResource()
	if err != nil {
		return nil, fmt.Errorf("factory resource: %w", err)
	}
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return nil, fmt.Errorf("factory resource: %w", err)
	}
	input := &unstructured.Unstructured{Object: data}
	if input.GetResourceVersion() == "" {
		return nil, fmt.Errorf("%w: factory resourceVersion is required", ErrStaleInput)
	}
	result, err := r.Workload(ctx, reference(input))
	if err != nil {
		if errors.Is(err, ownership.ErrUIDMismatch) {
			return nil, fmt.Errorf("%w: %w", ErrStaleInput, err)
		}
		return nil, err
	}
	if result.object.GetResourceVersion() != input.GetResourceVersion() || !reflect.DeepEqual(result.definition, factory.GetKarta()) {
		return nil, fmt.Errorf("%w: factory object or definition differs from observation", ErrStaleInput)
	}
	return result, nil
}

func (r *Resolver) acquire(ctx context.Context, seed ObjectRef, requireRoot bool) (*Workload, error) {
	if r == nil || r.reader == nil {
		return nil, ErrNoClient
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRef(seed); err != nil {
		return nil, err
	}
	if index, ok := r.reader.(*Index); ok {
		snapshot, err := index.snapshotWorkload(ctx, seed, requireRoot, r.maxDepth)
		if err != nil {
			return nil, err
		}
		return buildWorkload(ctx, snapshot.object, snapshot.pods, r.plans[snapshot.object.GroupVersionKind()], snapshot.observation)
	}
	if indexed, ok := r.reader.(IndexedReader); ok {
		capture, err := indexed.Capture(ctx, seed)
		if err != nil {
			return nil, err
		}
		return r.fromCapture(ctx, seed, requireRoot, capture)
	}
	started := time.Now()
	c := newCollection(r.reader)
	object, err := c.get(ctx, seed)
	if err != nil {
		return nil, err
	}
	if reference(object) != seed {
		return nil, fmt.Errorf("%w: seed identity or namespace differs", ownership.ErrScope)
	}
	path, err := c.trace(ctx, object, r.maxDepth)
	if err != nil {
		return nil, err
	}
	root := path[len(path)-1].Ref
	if requireRoot && root.UID != seed.UID {
		return nil, ErrNotWorkloadRoot
	}
	// The initial trace retains the full root. Further owner reads only need
	// metadata to establish membership and validate observation consistency.
	c.metadataOnly = true
	scope := root.Namespace
	data, err := collect(ctx, c, scope, r.maxDepth, root)
	if err != nil {
		return nil, err
	}
	// A requested Pod must occur in the complete list. Its disappearance cannot
	// be represented by an apparently successful observation of its old owner.
	if seed.GVK == podGVK && c.podUIDs[seed.UID] == nil {
		return nil, fmt.Errorf("%w: seed Pod disappeared during collection", ErrStaleInput)
	}

	members := make([]*corev1.Pod, 0)
	for index, pod := range data.Pods {
		if c.podRoots[index] == root.UID {
			members = append(members, pod)
		}
	}
	if len(members) < len(data.Pods) {
		// A partial workload must not retain the allocation for the complete Pod list.
		for i, pod := range members {
			members[i] = pod.DeepCopy()
		}
	} else if _, transferred := r.reader.(PodTransferReader); transferred {
		if err := compactPodArray(ctx, members); err != nil {
			return nil, err
		}
	}
	rootObject, err := c.get(ctx, root)
	if err != nil {
		return nil, err
	}
	sort.Slice(members, func(i, j int) bool { return podLess(members[i], members[j]) })
	observation := Observation{Source: "direct", Namespace: scope, Started: started, Completed: time.Now(), PodResourceVersion: data.PodResourceVersion}
	return buildWorkload(ctx, rootObject, members, r.plans[root.GVK], observation)
}

// capturedReader serves only one detached observation. Missing data cannot
// cause a fallback to the network, which would mix index generations.
type capturedReader struct {
	objects  map[objectKey]*unstructured.Unstructured
	mappings map[schema.GroupVersionKind]*meta.RESTMapping
}

func (c *capturedReader) Mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mapping, ok := c.mappings[gvk]
	if !ok {
		return nil, fmt.Errorf("%w: missing mapping for %s", ErrIncompleteOwnership, gvk)
	}
	return mapping, nil
}
func (c *capturedReader) Get(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	object, ok := c.objects[objectKey{gvk: ref.GVK, namespace: ref.Namespace, name: ref.Name}]
	if !ok {
		return nil, &ownership.Error{Kind: ownership.ErrMissing, Expected: ref}
	}
	return object, nil
}
func (c *capturedReader) ListPods(context.Context, string) (*corev1.PodList, error) {
	return nil, fmt.Errorf("%w: capture cannot acquire another list", ErrIncompleteOwnership)
}

func (r *Resolver) fromCapture(ctx context.Context, seed ObjectRef, requireRoot bool, capture *Capture) (*Workload, error) {
	if capture == nil || capture.Seed != seed {
		return nil, fmt.Errorf("%w: mismatched capture seed", ErrInvalidInput)
	}
	cr := &capturedReader{objects: make(map[objectKey]*unstructured.Unstructured), mappings: make(map[schema.GroupVersionKind]*meta.RESTMapping)}
	for key, mapping := range capture.Mappings {
		owned, err := copyMapping(mapping)
		if err != nil {
			return nil, err
		}
		if owned.GroupVersionKind != key {
			return nil, fmt.Errorf("%w: mismatched capture mapping", ErrInvalidInput)
		}
		cr.mappings[key] = owned
	}
	objects := make([]*unstructured.Unstructured, 0, len(capture.Objects)+len(capture.Pods))
	for _, object := range capture.Objects {
		if object == nil {
			return nil, fmt.Errorf("%w: nil captured object", ErrInvalidInput)
		}
		objects = append(objects, object.DeepCopy())
	}
	pods := make([]*corev1.Pod, len(capture.Pods))
	for i, pod := range capture.Pods {
		if pod == nil {
			return nil, fmt.Errorf("%w: nil captured Pod", ErrInvalidInput)
		}
		pods[i] = pod.DeepCopy()

	}
	seen := make(map[types.UID]*unstructured.Unstructured, len(objects))
	for _, object := range objects {
		ref := reference(object)
		if _, err := ownershipObject(object, cr.mappings[ref.GVK]); err != nil {
			return nil, err
		}
		if prior, ok := seen[ref.UID]; ok {
			priorRef := reference(prior)
			if priorRef.GVK.GroupKind() != ref.GVK.GroupKind() || priorRef.Namespace != ref.Namespace || priorRef.Name != ref.Name || prior.GetResourceVersion() != object.GetResourceVersion() || !sameOwnerReferences(prior.GetOwnerReferences(), object.GetOwnerReferences()) {
				return nil, fmt.Errorf("%w: inconsistent captured aliases for UID %s", ErrInvalidInput, ref.UID)
			}
		}
		seen[ref.UID] = object
		key := objectKey{gvk: ref.GVK, namespace: ref.Namespace, name: ref.Name}
		if _, ok := cr.objects[key]; ok {
			return nil, fmt.Errorf("%w: duplicate captured object %s", ErrInvalidInput, ref.Name)
		}
		cr.objects[key] = object
	}

	c := newCollection(cr)
	c.objects = cr.objects
	c.mappings = cr.mappings
	c.identities = seen
	for _, pod := range pods {
		if _, err := c.podIdentity(ctx, pod); err != nil {
			return nil, err
		}
		key := objectKey{podGVK, pod.Namespace, pod.Name}
		if c.pods[key] != nil || c.podUIDs[pod.UID] != nil {
			return nil, fmt.Errorf("%w: duplicate captured Pod", ErrInvalidInput)
		}
		if prior := seen[pod.UID]; prior != nil {
			if reference(prior) != podReference(pod) || prior.GetResourceVersion() != pod.ResourceVersion || !sameOwnerReferences(prior.GetOwnerReferences(), pod.OwnerReferences) {
				return nil, fmt.Errorf("%w: captured Pod conflicts with object", ErrStaleInput)
			}
		}
		c.pods[key] = pod
		c.podUIDs[pod.UID] = pod
	}
	for _, object := range objects {
		if object.GroupVersionKind() == podGVK && c.podUIDs[object.GetUID()] == nil {
			return nil, fmt.Errorf("%w: captured owner Pod absent from complete Pod set", ErrIncompleteOwnership)
		}
	}
	var path []ownership.Object
	var err error
	if seed.GVK == podGVK {
		pod := c.pods[objectKey{podGVK, seed.Namespace, seed.Name}]
		if pod == nil {
			return nil, fmt.Errorf("%w: seed Pod absent", ErrIncompleteOwnership)
		}
		if pod.UID != seed.UID {
			return nil, &ownership.Error{Kind: ownership.ErrUIDMismatch, Expected: seed, Actual: podReference(pod)}
		}
		path, err = c.tracePod(ctx, pod, r.maxDepth)
	} else {
		var object *unstructured.Unstructured
		object, err = c.get(ctx, seed)
		if err == nil {
			if reference(object) != seed {
				return nil, fmt.Errorf("%w: seed identity or namespace differs", ownership.ErrScope)
			}
			path, err = c.trace(ctx, object, r.maxDepth)
		}
	}
	if err != nil {
		return nil, err
	}
	root := path[len(path)-1].Ref
	if requireRoot && seed.UID != root.UID {
		return nil, ErrNotWorkloadRoot
	}
	if capture.Observation.Namespace != "" && capture.Observation.Namespace != root.Namespace {
		return nil, ErrInsufficientScope
	}
	var members []*corev1.Pod
	for _, pod := range pods {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if capture.Observation.Namespace != "" && pod.Namespace != capture.Observation.Namespace {
			return nil, ErrInsufficientScope
		}
		chain, err := c.tracePod(ctx, pod, r.maxDepth)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrIncompleteOwnership, err)
		}
		if chain[len(chain)-1].Ref.UID == root.UID {
			members = append(members, pod)
		}
	}
	rootObject, err := c.get(ctx, root)
	if err != nil {
		return nil, err
	}
	sort.Slice(members, func(i, j int) bool { return podLess(members[i], members[j]) })
	return buildWorkload(ctx, rootObject, members, r.plans[root.GVK], capture.Observation)
}
