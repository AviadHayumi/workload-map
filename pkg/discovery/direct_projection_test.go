// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type directProjectionReader struct {
	collectionCopyReader
	gets     map[ObjectRef]int
	returned map[ObjectRef]*unstructured.Unstructured
}

func (r *directProjectionReader) Get(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	r.gets[ref]++
	object, err := r.collectionCopyReader.Get(ctx, ref)
	if err == nil {
		r.returned[ref] = object
	}
	return object, err
}

func directProjectionFixture(t *testing.T) (*directProjectionReader, []*unstructured.Unstructured) {
	t.Helper()
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	root.Object["spec"] = map[string]any{"nested": map[string]any{"value": "original"}}
	root.Object["status"] = map[string]any{"observed": "original"}
	root.Object["extension"] = map[string]any{"nested": []any{map[string]any{"value": "original"}}}
	root.Object["metadata"].(map[string]any)["extension"] = map[string]any{"nested": []any{map[string]any{"value": "original"}}}
	root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "non-controller", UID: "non-controller"}})
	root.Object["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)["extension"] = map[string]any{"nested": []any{"retained"}}
	alias := root.DeepCopy()
	aliasGVK := indexBundleGVK
	aliasGVK.Version = "v2"
	alias.SetGroupVersionKind(aliasGVK)
	alias.Object["spec"] = map[string]any{"converted": "served-v2"}
	child := indexTestObject("child", "team", indexWorkerGVK, root)
	child.Object["spec"] = map[string]any{"initial": true}
	child.Object["extension"] = []any{"original"}
	other := indexTestObject("other", "team", indexBundleGVK, nil)
	other.Object["spec"] = map[string]any{"unrelated": true}
	other.Object["status"] = map[string]any{"observed": true}
	other.Object["extension"] = map[string]any{"value": "original"}
	pods := []*corev1.Pod{indexTestPod("first", "team", child), indexTestPod("second", "team", alias), indexTestPod("unrelated", "team", other)}
	for _, pod := range pods {
		pod.Labels = map[string]string{"observation": "original"}
		pod.Spec.Containers = []corev1.Container{{Name: "main", Image: "ghcr.io/example/workload:v1"}}
		pod.Status.Phase = corev1.PodRunning
	}
	seed, err := podObject(pods[0])
	if err != nil {
		t.Fatal(err)
	}
	objects := []*unstructured.Unstructured{root, child, seed, alias, other}
	reader := &directProjectionReader{
		collectionCopyReader: collectionCopyReader{
			capturedReader: capturedReader{
				objects: make(map[objectKey]*unstructured.Unstructured),
				mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
					podGVK:         indexTestMapping(podGVK, meta.RESTScopeNamespace),
					indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace),
					indexWorkerGVK: indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace),
					aliasGVK:       indexTestMapping(aliasGVK, meta.RESTScopeNamespace),
				},
			},
			pods: pods,
		},
		gets: make(map[ObjectRef]int), returned: make(map[ObjectRef]*unstructured.Unstructured),
	}
	for _, object := range objects {
		ref := reference(object)
		reader.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}] = object
	}
	return reader, objects
}

func TestDirectCollectionProjectsOnlyNewOwnerBodies(t *testing.T) {
	reader, objects := directProjectionFixture(t)
	ctx := context.Background()
	c := newCollection(reader)
	seed, err := c.get(ctx, reference(objects[2]))
	if err != nil {
		t.Fatal(err)
	}
	path, err := c.trace(ctx, seed, 32)
	if err != nil {
		t.Fatal(err)
	}
	root := path[len(path)-1].Ref
	c.metadataOnly = true
	if _, err := collect(ctx, c, "team", 32, root); err != nil {
		t.Fatal(err)
	}
	for i, original := range objects {
		ref := reference(original)
		actual := c.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}]
		if i < 3 {
			if !reflect.DeepEqual(actual, original) {
				t.Fatalf("initial object %s lost its full body", ref.Name)
			}
			continue
		}
		metadata := map[string]any{"name": ref.Name, "namespace": ref.Namespace, "uid": string(ref.UID), "resourceVersion": "1"}
		if i == 3 {
			metadata["ownerReferences"] = original.Object["metadata"].(map[string]any)["ownerReferences"]
		}
		want := map[string]any{"apiVersion": original.Object["apiVersion"], "kind": original.Object["kind"], "metadata": metadata}
		if !reflect.DeepEqual(actual.Object, want) {
			t.Fatalf("new owner %s projection = %#v, want raw ownership fields %#v", ref.Name, actual.Object, want)
		}
	}
	aliasRef := reference(objects[3])
	alias := c.objects[objectKey{aliasRef.GVK, aliasRef.Namespace, aliasRef.Name}]
	reader.returned[aliasRef].Object["metadata"].(map[string]any)["extension"].(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "changed"
	reader.returned[aliasRef].Object["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)["extension"].(map[string]any)["nested"].([]any)[0] = "changed"
	if !reflect.DeepEqual(alias.Object["metadata"].(map[string]any)["ownerReferences"], objects[3].Object["metadata"].(map[string]any)["ownerReferences"]) {
		t.Fatal("reader mutation changed retained raw owner references")
	}
	actualRoot, err := c.get(ctx, root)
	if err != nil || !reflect.DeepEqual(actualRoot, objects[0]) || reader.gets[root] != 1 {
		t.Fatalf("root lookup = %#v, %v, reads = %d, want original full v1 root and one read", actualRoot, err, reader.gets[root])
	}
	if c.identities[root.UID] != alias {
		t.Fatal("fixture did not exercise replacement of the UID alias slot")
	}
}

func TestDirectWorkloadRetainsFullRootAndSelectedPods(t *testing.T) {
	for _, test := range []struct {
		name    string
		fromPod bool
		empty   bool
		defined bool
	}{
		{name: "generic_pod", fromPod: true},
		{name: "generic_root"},
		{name: "generic_empty", empty: true},
		{name: "defined_pod", fromPod: true, defined: true},
		{name: "defined_root", defined: true},
		{name: "defined_empty", empty: true, defined: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, objects := directProjectionFixture(t)
			root := objects[0]
			wantPods := []*corev1.Pod{reader.pods[0].DeepCopy(), reader.pods[1].DeepCopy()}
			if test.empty {
				reader.pods = nil
				wantPods = []*corev1.Pod{}
			}
			reader.onGet = func(ref ObjectRef) {
				if ref != reference(objects[4]) {
					return
				}
				reader.returned[reference(root)].Object["spec"].(map[string]any)["nested"].(map[string]any)["value"] = "changed during acquisition"
				reader.returned[reference(objects[3])].Object["metadata"].(map[string]any)["extension"].(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "changed during acquisition"
			}
			options := Options{}
			if test.defined {
				definition := kartas.Deployment()
				definition.Spec.StructureDefinition.RootComponent.Kind = &v1alpha1.GroupVersionKind{Group: indexBundleGVK.Group, Version: indexBundleGVK.Version, Kind: indexBundleGVK.Kind}
				options.Definitions = []*v1alpha1.Karta{definition}
			}
			resolver, err := New(reader, options)
			if err != nil {
				t.Fatal(err)
			}
			var workload *Workload
			if test.fromPod {
				workload, err = resolver.WorkloadForPod(context.Background(), podReference(reader.pods[0]))
			} else {
				workload, err = resolver.Workload(context.Background(), reference(root))
			}
			if err != nil {
				t.Fatal(err)
			}
			if workload.Generic() == test.defined || workload.Root() != reference(root) || !reflect.DeepEqual(workload.Object(), root) {
				t.Fatal("workload did not preserve the full requested root version and definition")
			}
			if !reflect.DeepEqual(workload.Pods(), wantPods) {
				t.Fatal("workload did not preserve the full selected Pods")
			}
			if reader.gets[reference(root)] != 1 {
				t.Fatalf("root reads = %d, want 1", reader.gets[reference(root)])
			}
			reader.returned[reference(root)].Object["status"].(map[string]any)["observed"] = "changed after acquisition"
			reader.returned[reference(root)].Object["extension"].(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "changed after acquisition"
			for i := range reader.listed.Items {
				reader.listed.Items[i].Spec.Containers[0].Image = "ghcr.io/example/workload:v2"
				reader.listed.Items[i].Status.Phase = corev1.PodFailed
			}
			workload.Object().Object["spec"].(map[string]any)["nested"].(map[string]any)["value"] = "changed accessor"
			if !reflect.DeepEqual(workload.Object(), root) || !reflect.DeepEqual(workload.Pods(), wantPods) {
				t.Fatal("reader or accessor mutation changed the immutable observation")
			}
		})
	}
}

func TestPublicCollectRetainsFullOwnerBodies(t *testing.T) {
	reader, _ := directProjectionFixture(t)
	dataset, err := Collect(context.Background(), reader, "team")
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Objects) != 4 {
		t.Fatalf("collected objects = %d, want 4", len(dataset.Objects))
	}
	for _, actual := range dataset.Objects {
		ref := reference(actual)
		original := reader.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}]
		if !reflect.DeepEqual(actual, original) {
			t.Fatalf("Collect truncated %s %s", ref.GVK, ref.Name)
		}
		reader.returned[ref].Object["spec"].(map[string]any)["mutated"] = true
		if !reflect.DeepEqual(actual, original) {
			t.Fatalf("reader mutation changed collected %s %s", ref.GVK, ref.Name)
		}
	}
}

func TestDirectProjectionPreservesAliasValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
		want   error
	}{
		{name: "resource_version", mutate: func(object *unstructured.Unstructured) { object.SetResourceVersion("2") }, want: ErrStaleInput},
		{name: "owners", mutate: func(object *unstructured.Unstructured) { object.SetOwnerReferences(nil) }, want: ErrStaleInput},
		{name: "kind", mutate: func(object *unstructured.Unstructured) { object.SetKind("Different") }, want: ErrStaleInput},
		{name: "namespace", mutate: func(object *unstructured.Unstructured) { object.SetNamespace("elsewhere") }, want: ErrStaleInput},
		{name: "uid", mutate: func(object *unstructured.Unstructured) { object.SetUID("replacement") }, want: ownership.ErrUIDMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, objects := directProjectionFixture(t)
			test.mutate(objects[3])
			resolver, err := New(reader, Options{})
			if err != nil {
				t.Fatal(err)
			}
			workload, err := resolver.Workload(context.Background(), reference(objects[0]))
			if workload != nil || !errors.Is(err, test.want) {
				t.Fatalf("Workload() = %v, %v, want nil, %v", workload, err, test.want)
			}
		})
	}
}

func TestDirectProjectionRejectsLaterPodIdentityConflicts(t *testing.T) {
	for _, order := range []string{"pod_first", "alias_first"} {
		t.Run(order, func(t *testing.T) {
			reader, objects := directProjectionFixture(t)
			ownerPod := indexTestPod("owner", "team", nil)
			alias := indexTestObject("owner", "team", indexWorkerGVK, nil)
			child := indexTestPod("child-pod", "team", alias)
			reader.objects[objectKey{indexWorkerGVK, "team", "owner"}] = alias
			reader.pods = []*corev1.Pod{reader.pods[0], ownerPod, child}
			if order == "alias_first" {
				reader.pods[1], reader.pods[2] = reader.pods[2], reader.pods[1]
			}
			resolver, err := New(reader, Options{})
			if err != nil {
				t.Fatal(err)
			}
			workload, err := resolver.Workload(context.Background(), reference(objects[0]))
			if workload != nil || !errors.Is(err, ErrStaleInput) {
				t.Fatalf("Workload() = %v, %v, want nil, ErrStaleInput", workload, err)
			}
		})
	}
}

func TestDirectProjectionMalformedRetainedValueParity(t *testing.T) {
	for _, field := range [][]string{
		{"apiVersion"}, {"kind"}, {"metadata"},
		{"metadata", "name"}, {"metadata", "namespace"}, {"metadata", "uid"},
		{"metadata", "resourceVersion"}, {"metadata", "ownerReferences"},
	} {
		for _, value := range []struct {
			name   string
			data   any
			absent bool
		}{
			{name: "absent", absent: true},
			{name: "null"},
			{name: "number", data: int64(7)},
			{name: "object", data: map[string]any{"unexpected": []any{"value"}}},
			{name: "nil_object", data: map[string]any(nil)},
			{name: "list", data: []any{map[string]any{"controller": "invalid", "extension": []any{"retained"}}}},
			{name: "invalid_controller", data: []any{map[string]any{"controller": true, "apiVersion": int64(7), "kind": "Bundle", "name": "owner", "uid": "owner"}}},
		} {
			t.Run(strings.Join(field, "_")+"_"+value.name, func(t *testing.T) {
				for _, alias := range []bool{false, true} {
					root := indexTestObject("root", "team", indexBundleGVK, nil)
					object := root.DeepCopy()
					if alias {
						object.SetAPIVersion(indexBundleGVK.Group + "/v2")
					}
					ref := reference(object)
					if value.absent {
						unstructured.RemoveNestedField(object.Object, field...)
					} else if err := unstructured.SetNestedField(object.Object, value.data, field...); err != nil {
						t.Fatal(err)
					}
					reader := &collectionCopyReader{capturedReader: capturedReader{
						objects: map[objectKey]*unstructured.Unstructured{
							{gvk: ref.GVK, namespace: ref.Namespace, name: ref.Name}: object,
						},
						mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
							ref.GVK: indexTestMapping(ref.GVK, meta.RESTScopeNamespace),
						},
					}}
					if alias {
						reader.objects[objectKey{indexBundleGVK, "team", "root"}] = root
						reader.mappings[indexBundleGVK] = indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace)
					}
					var fullPath []ownership.Object
					var fullErr error
					for _, projected := range []bool{false, true} {
						c := newCollection(reader)
						if alias {
							if _, err := c.get(context.Background(), reference(root)); err != nil {
								t.Fatal(err)
							}
						}
						c.metadataOnly = projected
						actual, err := c.get(context.Background(), ref)
						var path []ownership.Object
						if err == nil {
							path, err = c.trace(context.Background(), actual, 32)
						}
						if !projected {
							fullPath, fullErr = path, err
							continue
						}
						if !reflect.DeepEqual(path, fullPath) || !reflect.DeepEqual(err, fullErr) {
							t.Fatalf("alias=%v: projected trace = %+v, %v; full trace = %+v, %v", alias, path, err, fullPath, fullErr)
						}
						if actual != nil {
							got, gotFound, gotErr := unstructured.NestedFieldNoCopy(actual.Object, field...)
							want, wantFound, wantErr := unstructured.NestedFieldNoCopy(object.Object, field...)
							if !reflect.DeepEqual(got, want) || gotFound != wantFound || !reflect.DeepEqual(gotErr, wantErr) {
								t.Fatalf("alias=%v: raw field changed: got %#v, %v, %v; want %#v, %v, %v", alias, got, gotFound, gotErr, want, wantFound, wantErr)
							}
						}
					}
				}
			})
		}
	}
}
