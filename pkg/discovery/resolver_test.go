// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

var (
	sdkPodGVK        = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	sdkDeploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	sdkReplicaSetGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}
	sdkBundleGVK     = schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Bundle"}
)

type sdkReader struct {
	objects       map[string]*unstructured.Unstructured
	mappings      map[schema.GroupVersionKind]*meta.RESTMapping
	pods          []*corev1.Pod
	calls         []string
	namespaces    []string
	getErrors     map[types.UID]error
	mappingErrors map[schema.GroupVersionKind]error
	listError     error
	mutateList    func(*corev1.PodList)
}

type sdkCapturedReader struct {
	*sdkReader
	capture      *discovery.Capture
	captureError error
	captures     int
}

func sdkKey(gvk schema.GroupVersionKind, namespace, name string) string {
	return gvk.String() + "/" + namespace + "/" + name
}

func sdkRef(object *unstructured.Unstructured) discovery.ObjectRef {
	return discovery.ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
}

func sdkPodRef(pod *corev1.Pod) discovery.ObjectRef {
	return discovery.ObjectRef{GVK: sdkPodGVK, Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID}
}

func sdkObject(gvk schema.GroupVersionKind, namespace, name string, uid types.UID, owner *unstructured.Unstructured) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind,
		"metadata": map[string]any{"name": name, "namespace": namespace, "uid": string(uid), "resourceVersion": "1"},
		"spec":     map[string]any{"replicas": int64(1), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "example.invalid/workload:v1"}}}}},
	}}
	if owner != nil {
		object.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(owner)})
	}
	return object
}

func sdkOwner(object *unstructured.Unstructured) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Name: object.GetName(), UID: object.GetUID(), Controller: ptr.To(true)}
}

func sdkPod(name string, owner *unstructured.Unstructured, labels map[string]string) *corev1.Pod {
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "team", UID: types.UID("uid-" + name), ResourceVersion: "1", Labels: labels,
	}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "example.invalid/workload:v1"}}}}
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{sdkOwner(owner)}
	}
	return pod
}

func sdkNewReader(t *testing.T, objects []*unstructured.Unstructured, pods ...*corev1.Pod) *sdkReader {
	t.Helper()
	r := &sdkReader{objects: make(map[string]*unstructured.Unstructured), mappings: make(map[schema.GroupVersionKind]*meta.RESTMapping), pods: pods,
		getErrors: make(map[types.UID]error), mappingErrors: make(map[schema.GroupVersionKind]error)}
	r.addMapping(sdkPodGVK, meta.RESTScopeNamespace)
	for _, object := range objects {
		r.addObject(object)
	}
	for _, pod := range pods {
		value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
		if err != nil {
			t.Fatal(err)
		}
		object := &unstructured.Unstructured{Object: value}
		object.SetGroupVersionKind(sdkPodGVK)
		r.addObject(object)
	}
	return r
}

func (r *sdkReader) addMapping(gvk schema.GroupVersionKind, scope meta.RESTScope) {
	r.mappings[gvk] = &meta.RESTMapping{GroupVersionKind: gvk, Resource: gvk.GroupVersion().WithResource(strings.ToLower(gvk.Kind) + "s"), Scope: scope}
}

func (r *sdkReader) addObject(object *unstructured.Unstructured) {
	scope := meta.RESTScopeNamespace
	if object.GetNamespace() == "" {
		scope = meta.RESTScopeRoot
	}
	r.addMapping(object.GroupVersionKind(), scope)
	r.objects[sdkKey(object.GroupVersionKind(), object.GetNamespace(), object.GetName())] = object
}

func (r *sdkReader) Mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	r.calls = append(r.calls, "map:"+gvk.String())
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.mappingErrors[gvk]; err != nil {
		return nil, err
	}
	mapping := r.mappings[gvk]
	if mapping == nil {
		return nil, &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
	}
	copy := *mapping
	return &copy, nil
}

func (r *sdkReader) Get(ctx context.Context, ref discovery.ObjectRef) (*unstructured.Unstructured, error) {
	r.calls = append(r.calls, "get:"+string(ref.UID))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.getErrors[ref.UID]; err != nil {
		return nil, err
	}
	object := r.objects[sdkKey(ref.GVK, ref.Namespace, ref.Name)]
	if object == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: ref.GVK.Group, Resource: strings.ToLower(ref.GVK.Kind)}, ref.Name)
	}
	return object.DeepCopy(), nil
}

func (r *sdkReader) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	r.calls = append(r.calls, "list:"+namespace)
	r.namespaces = append(r.namespaces, namespace)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.listError != nil {
		return nil, r.listError
	}
	list := &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "list-rv"}}
	for _, pod := range r.pods {
		if namespace == "" || pod.Namespace == namespace {
			list.Items = append(list.Items, *pod.DeepCopy())
		}
	}
	if r.mutateList != nil {
		r.mutateList(list)
	}
	return list, nil
}

func (r *sdkCapturedReader) Capture(ctx context.Context, _ discovery.ObjectRef) (*discovery.Capture, error) {
	r.captures++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.capture, r.captureError
}

func sdkResolver(t *testing.T, reader discovery.Reader, definitions ...*v1alpha1.Karta) *discovery.Resolver {
	t.Helper()
	resolver, err := discovery.New(reader, discovery.Options{Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func sdkUIDs(pods []*corev1.Pod) []string {
	ids := make([]string, len(pods))
	for i, pod := range pods {
		ids[i] = string(pod.UID)
	}
	slices.Sort(ids)
	return ids
}

func sdkAssertPods(t *testing.T, pods []*corev1.Pod, want ...string) {
	t.Helper()
	expected := append([]string{}, want...)
	slices.Sort(expected)
	if got := sdkUIDs(pods); !reflect.DeepEqual(got, expected) {
		t.Fatalf("Pod UIDs = %v, want %v", got, expected)
	}
}

func sdkDeploymentFixture(t *testing.T) (*sdkReader, *unstructured.Unstructured, *unstructured.Unstructured, []*corev1.Pod) {
	t.Helper()
	root := sdkObject(sdkDeploymentGVK, "team", "train", "deployment", nil)
	old := sdkObject(sdkReplicaSetGVK, "team", "train-old", "rs-old", root)
	current := sdkObject(sdkReplicaSetGVK, "team", "train-new", "rs-new", root)
	other := sdkObject(sdkDeploymentGVK, "team", "other", "other", nil)
	pods := []*corev1.Pod{sdkPod("running", current, map[string]string{"app": "shared"}), sdkPod("completed", old, nil), sdkPod("failed", old, nil), sdkPod("terminating", current, nil), sdkPod("unscheduled", current, nil), sdkPod("unrelated", other, map[string]string{"app": "shared"})}
	pods[0].Status.Phase = corev1.PodRunning
	pods[1].Status.Phase = corev1.PodSucceeded
	pods[2].Status.Phase = corev1.PodFailed
	now := metav1.NewTime(time.Now())
	pods[3].DeletionTimestamp = &now
	pods[4].Status.Phase = corev1.PodPending
	pods[4].Spec.NodeName = ""
	return sdkNewReader(t, []*unstructured.Unstructured{root, old, current, other}, pods...), root, current, pods
}

func TestResolverObservesWholeWorkloadAcrossPodStates(t *testing.T) {
	reader, root, replicaSet, pods := sdkDeploymentFixture(t)
	resolver := sdkResolver(t, reader)
	if len(reader.calls) != 0 {
		t.Fatal("constructor accessed reader")
	}
	workload, err := resolver.WorkloadForPod(context.Background(), sdkPodRef(pods[0]))
	if err != nil {
		t.Fatal(err)
	}
	if workload.Root() != sdkRef(root) || workload.Generic() {
		t.Fatalf("root = %+v, generic = %v", workload.Root(), workload.Generic())
	}
	sdkAssertPods(t, workload.Pods(), "uid-running", "uid-completed", "uid-failed", "uid-terminating", "uid-unscheduled")
	if !reflect.DeepEqual(reader.namespaces, []string{"team"}) {
		t.Fatalf("list scopes = %v", reader.namespaces)
	}
	observation := workload.Observation()
	if observation.Source != "direct" || observation.Namespace != "team" || observation.PodResourceVersion != "list-rv" || observation.Started.IsZero() || observation.Completed.Before(observation.Started) {
		t.Fatalf("observation = %+v", observation)
	}
	component, err := workload.Component("deployment")
	if err != nil {
		t.Fatal(err)
	}
	members, err := component.Pods()
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, members, "uid-running", "uid-completed", "uid-failed", "uid-terminating", "uid-unscheduled")
	if _, err := resolver.Workload(context.Background(), sdkRef(replicaSet)); !errors.Is(err, discovery.ErrNotWorkloadRoot) {
		t.Fatalf("intermediate owner = %v", err)
	}
}

func TestUnknownTerminalWrapperIncludesKnownSiblingBranches(t *testing.T) {
	bundle := sdkObject(sdkBundleGVK, "team", "bundle", "bundle", nil)
	first := sdkObject(sdkDeploymentGVK, "team", "first", "first", bundle)
	second := sdkObject(sdkDeploymentGVK, "team", "second", "second", bundle)
	p1, p2 := sdkPod("first", first, nil), sdkPod("second", second, nil)
	r := sdkResolver(t, sdkNewReader(t, []*unstructured.Unstructured{bundle, first, second}, p1, p2))
	w, err := r.WorkloadForPod(context.Background(), sdkPodRef(p1))
	if err != nil {
		t.Fatal(err)
	}
	if w.Root() != sdkRef(bundle) || !w.Generic() || w.Definition() != nil {
		t.Fatalf("terminal root not preserved: %+v", w.Root())
	}
	sdkAssertPods(t, w.Pods(), "uid-first", "uid-second")
	c, err := w.Component("pods")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := c.Instance("")
	if err != nil {
		t.Fatal(err)
	}
	members, err := instance.Pods()
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, members, "uid-first", "uid-second")
	if _, err := c.Replica("0"); !errors.Is(err, discovery.ErrNoReplicaAxis) {
		t.Fatalf("generic replica = %v", err)
	}
	if _, err := w.DesiredTree(context.Background()); !errors.Is(err, discovery.ErrUnsupported) {
		t.Fatalf("generic desired tree = %v", err)
	}
}

func TestExplicitEmptyRootAndStandalonePod(t *testing.T) {
	empty := sdkObject(sdkDeploymentGVK, "team", "empty", "empty", nil)
	pod := sdkPod("standalone", nil, nil)
	pod.Status.Phase = corev1.PodSucceeded
	r := sdkResolver(t, sdkNewReader(t, []*unstructured.Unstructured{empty}, pod))
	w, err := r.Workload(context.Background(), sdkRef(empty))
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, w.Pods())
	if w.Root() != sdkRef(empty) || w.Generic() {
		t.Fatalf("empty workload = %+v", w.Root())
	}
	w, err = r.WorkloadForPod(context.Background(), sdkPodRef(pod))
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, w.Pods(), "uid-standalone")
	if w.Root() != sdkPodRef(pod) {
		t.Fatalf("standalone root = %+v", w.Root())
	}
}

func TestNoClientCancellationAndInvalidInputAreOffline(t *testing.T) {
	var typedNil *sdkReader
	for _, reader := range []discovery.Reader{nil, typedNil} {
		r := sdkResolver(t, reader)
		if _, err := r.Workload(context.Background(), discovery.ObjectRef{}); !errors.Is(err, discovery.ErrNoClient) {
			t.Fatalf("Workload = %v", err)
		}
		if _, err := r.WorkloadForPod(context.Background(), discovery.ObjectRef{}); !errors.Is(err, discovery.ErrNoClient) {
			t.Fatalf("WorkloadForPod = %v", err)
		}
		if _, err := r.Snapshot(context.Background(), nil); !errors.Is(err, discovery.ErrNoClient) {
			t.Fatalf("Snapshot = %v", err)
		}
	}
	reader, root, _, pods := sdkDeploymentFixture(t)
	r := sdkResolver(t, reader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Workload(ctx, sdkRef(root)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled workload = %v", err)
	}
	if _, err := r.WorkloadForPod(ctx, sdkPodRef(pods[0])); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pod = %v", err)
	}
	ref := sdkRef(root)
	ref.UID = ""
	if _, err := r.Workload(context.Background(), ref); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("name-only ref = %v", err)
	}
	if _, err := r.WorkloadForPod(context.Background(), sdkRef(root)); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("non-Pod ref = %v", err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("offline failures accessed reader: %v", reader.calls)
	}
	if _, err := discovery.New(reader, discovery.Options{Definitions: []*v1alpha1.Karta{kartas.Deployment(), kartas.Deployment()}}); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("duplicate definition = %v", err)
	}
	if _, err := discovery.New(reader, discovery.Options{Definitions: []*v1alpha1.Karta{nil}}); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("nil definition = %v", err)
	}
	if _, err := discovery.New(reader, discovery.Options{MaxDepth: -1}); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("negative depth = %v", err)
	}
}

func TestOwnershipAndCollectionFailuresNeverReturnPartialWorkloads(t *testing.T) {
	mappingFailure := errors.New("mapping unavailable")
	tests := []struct {
		name      string
		mutate    func(*sdkReader, *unstructured.Unstructured, *unstructured.Unstructured, []*corev1.Pod)
		expected  error
		forbidden bool
		maxDepth  int
	}{
		{name: "missing owner", mutate: func(r *sdkReader, _ *unstructured.Unstructured, rs *unstructured.Unstructured, _ []*corev1.Pod) {
			delete(r.objects, sdkKey(rs.GroupVersionKind(), rs.GetNamespace(), rs.GetName()))
		}, expected: ownership.ErrMissing},
		{name: "forbidden owner", mutate: func(r *sdkReader, root, _ *unstructured.Unstructured, _ []*corev1.Pod) {
			r.getErrors[root.GetUID()] = apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, root.GetName(), errors.New("denied"))
		}, forbidden: true},
		{name: "mapping failure", mutate: func(r *sdkReader, _, _ *unstructured.Unstructured, _ []*corev1.Pod) {
			r.mappingErrors[sdkReplicaSetGVK] = mappingFailure
		}, expected: mappingFailure},
		{name: "owner name reused", mutate: func(_ *sdkReader, root, _ *unstructured.Unstructured, _ []*corev1.Pod) { root.SetUID("replacement") }, expected: ownership.ErrUIDMismatch},
		{name: "ownership cycle", mutate: func(_ *sdkReader, root, rs *unstructured.Unstructured, _ []*corev1.Pod) {
			root.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(rs)})
		}, expected: ownership.ErrCycle},
		{name: "depth exceeded", maxDepth: 1, mutate: func(*sdkReader, *unstructured.Unstructured, *unstructured.Unstructured, []*corev1.Pod) {}, expected: ownership.ErrDepthExceeded},
		{name: "unrelated missing owner", mutate: func(r *sdkReader, _, _ *unstructured.Unstructured, _ []*corev1.Pod) {
			delete(r.objects, sdkKey(sdkDeploymentGVK, "team", "other"))
		}, expected: discovery.ErrIncompleteOwnership},
		{name: "partial list", mutate: func(r *sdkReader, _, _ *unstructured.Unstructured, _ []*corev1.Pod) {
			r.mutateList = func(l *corev1.PodList) { l.Continue = "more" }
		}, expected: discovery.ErrIncompleteOwnership},
		{name: "changed seed", mutate: func(r *sdkReader, _, _ *unstructured.Unstructured, _ []*corev1.Pod) {
			r.mutateList = func(l *corev1.PodList) { l.Items[0].ResourceVersion = "2" }
		}, expected: discovery.ErrStaleInput},
		{name: "disappeared seed", mutate: func(r *sdkReader, _, _ *unstructured.Unstructured, _ []*corev1.Pod) {
			r.mutateList = func(l *corev1.PodList) { l.Items = l.Items[1:] }
		}, expected: discovery.ErrStaleInput},
		{name: "outside list scope", mutate: func(r *sdkReader, _, _ *unstructured.Unstructured, _ []*corev1.Pod) {
			r.mutateList = func(l *corev1.PodList) { l.Items[1].Namespace = "outside" }
		}, expected: discovery.ErrInsufficientScope},
		{name: "invalid owner scope", mutate: func(r *sdkReader, _, _ *unstructured.Unstructured, _ []*corev1.Pod) {
			r.mappings[sdkReplicaSetGVK].Scope = meta.RESTScopeRoot
		}, expected: ownership.ErrMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, root, rs, pods := sdkDeploymentFixture(t)
			test.mutate(reader, root, rs, pods)
			r, err := discovery.New(reader, discovery.Options{MaxDepth: test.maxDepth})
			if err != nil {
				t.Fatal(err)
			}
			w, err := r.WorkloadForPod(context.Background(), sdkPodRef(pods[0]))
			if w != nil || err == nil {
				t.Fatalf("partial result: workload=%v error=%v", w, err)
			}
			if test.forbidden {
				if !apierrors.IsForbidden(err) {
					t.Fatalf("forbidden cause lost: %v", err)
				}
			} else if !errors.Is(err, test.expected) {
				t.Fatalf("error = %v, want %v", err, test.expected)
			}
		})
	}
	reader, root, _, _ := sdkDeploymentFixture(t)
	expired := apierrors.NewResourceExpired("list token expired")
	reader.listError = expired
	if w, err := sdkResolver(t, reader).Workload(context.Background(), sdkRef(root)); w != nil || !errors.Is(err, expired) {
		t.Fatalf("expired list = %v, %v", w, err)
	}
}

func TestClusterRootUsesAllNamespaces(t *testing.T) {
	cluster := sdkObject(sdkBundleGVK, "", "cluster-bundle", "cluster", nil)
	first, second := sdkPod("one", cluster, nil), sdkPod("two", cluster, nil)
	second.Namespace = "another-team"
	reader := sdkNewReader(t, []*unstructured.Unstructured{cluster}, first, second)
	w, err := sdkResolver(t, reader).WorkloadForPod(context.Background(), sdkPodRef(first))
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, w.Pods(), "uid-one", "uid-two")
	if !reflect.DeepEqual(reader.namespaces, []string{""}) || w.Root().Namespace != "" {
		t.Fatalf("cluster list scope = %v, root = %+v", reader.namespaces, w.Root())
	}

	invalid := sdkObject(sdkBundleGVK, "", "invalid", "invalid", nil)
	reader = sdkNewReader(t, []*unstructured.Unstructured{invalid})
	reader.mappings[sdkBundleGVK].Scope = meta.RESTScopeNamespace
	if _, err := sdkResolver(t, reader).Workload(context.Background(), sdkRef(invalid)); !errors.Is(err, ownership.ErrScope) {
		t.Fatalf("inconsistent mapping scope = %v", err)
	}
}

func TestSnapshotChecksDefinitionUIDAndResourceVersion(t *testing.T) {
	reader, root, _, _ := sdkDeploymentFixture(t)
	r := sdkResolver(t, reader)
	factory := resource.NewComponentFactoryFromObject(kartas.Deployment(), root.DeepCopy())
	if _, err := r.Snapshot(context.Background(), factory); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*unstructured.Unstructured, *v1alpha1.Karta)
	}{
		{"resource version", func(o *unstructured.Unstructured, _ *v1alpha1.Karta) { o.SetResourceVersion("old") }},
		{"absent resource version", func(o *unstructured.Unstructured, _ *v1alpha1.Karta) { o.SetResourceVersion("") }},
		{"UID", func(o *unstructured.Unstructured, _ *v1alpha1.Karta) { o.SetUID("prior-lifetime") }},
		{"definition", func(_ *unstructured.Unstructured, k *v1alpha1.Karta) {
			k.Spec.StructureDefinition.RootComponent.Name = "different"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			object, definition := root.DeepCopy(), kartas.Deployment()
			test.change(object, definition)
			factory := resource.NewComponentFactoryFromObject(definition, object)
			if w, err := r.Snapshot(context.Background(), factory); w != nil || !errors.Is(err, discovery.ErrStaleInput) {
				t.Fatalf("snapshot = %v, %v", w, err)
			}
		})
	}
}

func TestGettersAreDetachedAndPerformNoRequests(t *testing.T) {
	reader, root, _, pods := sdkDeploymentFixture(t)
	definition := kartas.Deployment()
	r := sdkResolver(t, reader, definition)
	definition.Spec.StructureDefinition.RootComponent.Name = "mutated-input"
	w, err := r.Workload(context.Background(), sdkRef(root))
	if err != nil {
		t.Fatal(err)
	}
	before := len(reader.calls)
	root.SetName("changed-source")
	pods[0].Labels["app"] = "changed-source"
	returned := w.Pods()
	returned[0].Name = "changed-output"
	w.Object().SetName("changed-output")
	w.Definition().Spec.StructureDefinition.RootComponent.Name = "changed-output"
	_ = w.Root()
	_ = w.Generic()
	_ = w.Observation()
	_ = w.MembershipIssues()
	c, err := w.Component("deployment")
	if err != nil {
		t.Fatal(err)
	}
	members, err := c.Pods()
	if err != nil {
		t.Fatal(err)
	}
	members[0].Name = "changed-scope-output"
	instance, err := c.Instance("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Pods(); err != nil {
		t.Fatal(err)
	}
	if w.Root().Name != "train" || w.Object().GetName() != "train" || w.Definition().Spec.StructureDefinition.RootComponent.Name != "deployment" {
		t.Fatal("object or definition mutation escaped")
	}
	for _, pod := range w.Pods() {
		if pod.Name == "changed-output" || pod.Name == "changed-scope-output" || pod.Labels["app"] == "changed-source" {
			t.Fatal("Pod mutation escaped")
		}
	}
	if len(reader.calls) != before {
		t.Fatalf("getters accessed reader: %v", reader.calls[before:])
	}
}

func TestIndexedCaptureIsValidatedAndNeverFallsBack(t *testing.T) {
	reader, root, _, pods := sdkDeploymentFixture(t)
	objects := []*unstructured.Unstructured{}
	for _, object := range reader.objects {
		if object.GetKind() != "Pod" {
			objects = append(objects, object)
		}
	}
	capture := &discovery.Capture{Seed: sdkPodRef(pods[0]), Objects: objects, Pods: pods, Mappings: reader.mappings,
		Observation: discovery.Observation{Source: "indexed", Namespace: "team", Generation: 7}}
	indexed := &sdkCapturedReader{sdkReader: reader, capture: capture}
	r := sdkResolver(t, indexed)
	w, err := r.WorkloadForPod(context.Background(), sdkPodRef(pods[0]))
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, w.Pods(), "uid-running", "uid-completed", "uid-failed", "uid-terminating", "uid-unscheduled")
	if indexed.captures != 1 || len(reader.calls) != 0 || w.Observation().Generation != 7 {
		t.Fatalf("capture calls=%d direct=%v observation=%+v", indexed.captures, reader.calls, w.Observation())
	}
	root.SetName("changed-capture")
	capture.Pods[0].Name = "changed-capture"
	if w.Root().Name != "train" {
		t.Fatal("capture mutation escaped")
	}
	for _, pod := range w.Pods() {
		if pod.Name == "changed-capture" {
			t.Fatal("capture Pod mutation escaped")
		}
	}
	indexed.captureError = discovery.ErrNotSynced
	if w, err := r.WorkloadForPod(context.Background(), capture.Seed); w != nil || !errors.Is(err, discovery.ErrNotSynced) {
		t.Fatalf("capture error = %v, %v", w, err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("capture error fell back to direct calls: %v", reader.calls)
	}
}

func TestExactGVKDefinitionsAndLazyDesiredTree(t *testing.T) {
	root := sdkObject(sdkBundleGVK, "team", "custom", "custom", nil)
	pod := sdkPod("custom", root, nil)
	pod.Annotations = map[string]string{"role": "trainer"}
	pod.Status.Phase = corev1.PodRunning
	k := &v1alpha1.Karta{Spec: v1alpha1.KartaSpec{StructureDefinition: v1alpha1.StructureDefinition{RootComponent: v1alpha1.ComponentDefinition{
		Name: "custom", Kind: &v1alpha1.GroupVersionKind{Group: sdkBundleGVK.Group, Version: sdkBundleGVK.Version, Kind: sdkBundleGVK.Kind},
		SpecDefinition:   &v1alpha1.SpecDefinition{PodSpecPath: ptr.To(`error("desired spec unavailable")`)},
		ScaleDefinition:  &v1alpha1.ScaleDefinition{ReplicasPath: ptr.To(`error("desired scale unavailable")`)},
		StatusDefinition: &v1alpha1.StatusDefinition{PhaseDefinition: &v1alpha1.PhaseDefinition{Path: `error("status unavailable")`}},
		PodSelector:      &v1alpha1.PodSelector{ComponentTypeSelector: &v1alpha1.ComponentTypeSelector{KeyPath: `.metadata.annotations.role + ":" + .spec.containers[0].name + ":" + .status.phase`, Value: ptr.To("trainer:main:Running")}},
	}}}}
	reader := sdkNewReader(t, []*unstructured.Unstructured{root}, pod)
	r := sdkResolver(t, reader, k)
	w, err := r.Workload(context.Background(), sdkRef(root))
	if err != nil {
		t.Fatal(err)
	}
	if w.Generic() || len(w.MembershipIssues()) != 0 {
		t.Fatalf("membership evaluated desired state: %+v", w.MembershipIssues())
	}
	c, err := w.Component("custom")
	if err != nil {
		t.Fatal(err)
	}
	members, err := c.Pods()
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, members, "uid-custom")
	before := len(reader.calls)
	if _, err := w.DesiredTree(context.Background()); err == nil {
		t.Fatal("desired-tree error was hidden")
	}
	if len(reader.calls) != before {
		t.Fatal("DesiredTree fetched new data")
	}

	// An override for v1 cannot authorize jq against an unknown served v2 shape.
	version2 := root.DeepCopy()
	version2.SetAPIVersion("example.org/v2")
	version2.SetUID("version2")
	reader = sdkNewReader(t, []*unstructured.Unstructured{version2})
	w, err = sdkResolver(t, reader, k).Workload(context.Background(), sdkRef(version2))
	if err != nil {
		t.Fatal(err)
	}
	if !w.Generic() {
		t.Fatal("v1 definition leaked into v2")
	}
}

func TestCaptureRejectsMissingClosureAndScopeMismatch(t *testing.T) {
	for _, problem := range []string{"closure", "seed", "scope"} {
		t.Run(problem, func(t *testing.T) {
			reader, _, _, pods := sdkDeploymentFixture(t)
			objects := []*unstructured.Unstructured{}
			for _, object := range reader.objects {
				if object.GetKind() != "Pod" {
					objects = append(objects, object)
				}
			}
			capture := &discovery.Capture{Seed: sdkPodRef(pods[0]), Objects: objects, Pods: pods, Mappings: reader.mappings, Observation: discovery.Observation{Source: "indexed", Namespace: "team"}}
			var expected error
			switch problem {
			case "closure":
				capture.Objects = nil
				expected = ownership.ErrMissing
			case "seed":
				capture.Seed.UID = "wrong"
				expected = discovery.ErrInvalidInput
			case "scope":
				capture.Observation.Namespace = "elsewhere"
				expected = discovery.ErrInsufficientScope
			}
			indexed := &sdkCapturedReader{sdkReader: reader, capture: capture}
			w, err := sdkResolver(t, indexed).WorkloadForPod(context.Background(), sdkPodRef(pods[0]))
			if w != nil || !errors.Is(err, expected) {
				t.Fatalf("%s capture = %v, %v (want %v)", problem, w, err, expected)
			}
			if len(reader.calls) != 0 {
				t.Fatal("invalid capture fell back to direct calls")
			}
		})
	}
}

var _ discovery.Reader = (*sdkReader)(nil)
var _ discovery.IndexedReader = (*sdkCapturedReader)(nil)

func TestClusterDependentRejectsNamespacedController(t *testing.T) {
	namespaced := sdkObject(sdkDeploymentGVK, "team", "namespaced", "namespaced", nil)
	cluster := sdkObject(sdkBundleGVK, "", "cluster-child", "cluster-child", namespaced)
	pod := sdkPod("cluster-child", cluster, nil)
	reader := sdkNewReader(t, []*unstructured.Unstructured{cluster, namespaced}, pod)
	w, err := sdkResolver(t, reader).WorkloadForPod(context.Background(), sdkPodRef(pod))
	if w != nil || !errors.Is(err, ownership.ErrScope) {
		t.Fatalf("cluster-dependent/namespaced-owner result = %v, %v", w, err)
	}
}

func TestControllerReferencePolicy(t *testing.T) {
	reader, root, _, pods := sdkDeploymentFixture(t)
	pod := pods[0]
	observer := metav1.OwnerReference{APIVersion: "unused.example.org/v1", Kind: "Observer", Name: "missing", UID: "noncontroller", Controller: ptr.To(false)}
	pod.OwnerReferences = append([]metav1.OwnerReference{observer}, pod.OwnerReferences...)
	reader.objects[sdkKey(sdkPodGVK, pod.Namespace, pod.Name)].SetOwnerReferences(pod.OwnerReferences)
	r := sdkResolver(t, reader)
	w, err := r.WorkloadForPod(context.Background(), sdkPodRef(pod))
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, w.Pods(), "uid-running", "uid-completed", "uid-failed", "uid-terminating", "uid-unscheduled")
	for _, call := range reader.calls {
		if strings.Contains(call, "unused.example.org") || strings.Contains(call, "noncontroller") {
			t.Fatalf("noncontroller edge followed: %s", call)
		}
	}
	pod.OwnerReferences = append(pod.OwnerReferences, sdkOwner(root))
	reader.objects[sdkKey(sdkPodGVK, pod.Namespace, pod.Name)].SetOwnerReferences(pod.OwnerReferences)
	if w, err := r.WorkloadForPod(context.Background(), sdkPodRef(pod)); w != nil || !errors.Is(err, ownership.ErrMultipleControllers) {
		t.Fatalf("multiple controllers = %v, %v", w, err)
	}
	pod.OwnerReferences = []metav1.OwnerReference{sdkOwner(root)}
	pod.OwnerReferences[0].UID = ""
	reader.objects[sdkKey(sdkPodGVK, pod.Namespace, pod.Name)].SetOwnerReferences(pod.OwnerReferences)
	if w, err := r.WorkloadForPod(context.Background(), sdkPodRef(pod)); w != nil || !errors.Is(err, ownership.ErrInvalidReference) {
		t.Fatalf("malformed controller = %v, %v", w, err)
	}
}
