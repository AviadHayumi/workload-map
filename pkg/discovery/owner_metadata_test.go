// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type ownerMetadataTestReader struct {
	Reader
	metadata map[objectKey]*metav1.PartialObjectMetadata
	calls    map[ObjectRef]int
	onGet    func(ObjectRef)
	err      error
}

type ownerMetadataCaptureTestReader struct{ *ownerReferenceCaptureReader }

func newOwnerMetadataTestReader(reader Reader, objects ...*unstructured.Unstructured) *ownerMetadataTestReader {
	r := &ownerMetadataTestReader{Reader: reader, metadata: make(map[objectKey]*metav1.PartialObjectMetadata), calls: make(map[ObjectRef]int)}
	for _, object := range objects {
		ref := reference(object)
		r.metadata[objectKey{ref.GVK, ref.Namespace, ref.Name}] = &metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"},
			ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace, UID: ref.UID,
				ResourceVersion: object.GetResourceVersion(), OwnerReferences: object.GetOwnerReferences()},
		}
	}
	return r
}

func (r *ownerMetadataTestReader) GetOwnerMetadata(_ context.Context, ref ObjectRef) (*metav1.PartialObjectMetadata, error) {
	r.calls[ref]++
	if r.onGet != nil {
		r.onGet(ref)
	}
	return r.metadata[objectKey{ref.GVK, ref.Namespace, ref.Name}], r.err
}

func (r *ownerMetadataCaptureTestReader) GetOwnerMetadata(context.Context, ObjectRef) (*metav1.PartialObjectMetadata, error) {
	r.t.Fatal("capture called owner metadata")
	return nil, ErrInvalidInput
}

func TestOwnerMetadataAcquisitionPreservesFullRootsAndPods(t *testing.T) {
	for _, defined := range []bool{false, true} {
		for _, fromPod := range []bool{false, true} {
			name := map[bool]string{false: "generic", true: "defined"}[defined] + "/" + map[bool]string{false: "root", true: "pod"}[fromPod]
			t.Run(name, func(t *testing.T) {
				base, objects := directProjectionFixture(t)
				reader := newOwnerMetadataTestReader(base, objects...)
				options := Options{}
				if defined {
					definition := kartas.Deployment()
					definition.Spec.StructureDefinition.RootComponent.Kind = &v1alpha1.GroupVersionKind{Group: indexBundleGVK.Group, Version: indexBundleGVK.Version, Kind: indexBundleGVK.Kind}
					options.Definitions = []*v1alpha1.Karta{definition}
				}
				resolver, err := New(reader, options)
				if err != nil {
					t.Fatal(err)
				}
				var workload *Workload
				if fromPod {
					workload, err = resolver.WorkloadForPod(context.Background(), reference(objects[2]))
				} else {
					workload, err = resolver.Workload(context.Background(), reference(objects[0]))
				}
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(workload.Object(), objects[0]) || !reflect.DeepEqual(workload.Pods(), base.pods[:2]) || workload.Generic() == defined {
					t.Fatal("metadata acquisition changed full root, selected Pods, or definition")
				}
				wantFull, wantMetadata := 1, 3
				if fromPod {
					wantFull, wantMetadata = 3, 2
				}
				if len(base.gets) != wantFull || len(reader.calls) != wantMetadata {
					t.Fatalf("full reads = %v, metadata reads = %v", base.gets, reader.calls)
				}
				for ref, count := range reader.calls {
					if count != 1 || base.gets[ref] != 0 || ref.GVK == podGVK || ref == reference(objects[0]) {
						t.Fatalf("ineligible or repeated metadata read: %+v", ref)
					}
				}
				if base.gets[reference(objects[0])] != 1 {
					t.Fatal("full root was fetched again")
				}
				base.returned[reference(objects[0])].Object["spec"].(map[string]any)["nested"].(map[string]any)["value"] = "changed reader"
				base.listed.Items[0].Spec.Containers[0].Image = "changed reader"
				workload.Object().Object["extension"].(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "changed output"
				if !reflect.DeepEqual(workload.Object(), objects[0]) || !reflect.DeepEqual(workload.Pods(), base.pods[:2]) {
					t.Fatal("reader or accessor mutation changed result")
				}
			})
		}
	}
}

func TestOwnerMetadataPublicCollectAndCaptureDoNotDispatch(t *testing.T) {
	base, objects := directProjectionFixture(t)
	reader := newOwnerMetadataTestReader(base, objects...)
	reader.err = errors.New("metadata must not run")
	dataset, err := Collect(context.Background(), reader, "team")
	if err != nil || len(reader.calls) != 0 || len(dataset.Objects) != 4 {
		t.Fatalf("Collect = %+v, %v; metadata = %v", dataset, err, reader.calls)
	}
	for _, actual := range dataset.Objects {
		ref := reference(actual)
		if !reflect.DeepEqual(actual, base.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}]) {
			t.Fatal("public Collect projected an owner")
		}
	}
	index := NewIndex()
	if _, err := index.Replace(context.Background(), dataset); err != nil {
		t.Fatal(err)
	}
	capture, err := index.Capture(context.Background(), reference(objects[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []*Capture{capture, nil} {
		captured := &ownerMetadataCaptureTestReader{&ownerReferenceCaptureReader{t: t, capture: value}}
		resolver, err := New(captured, Options{})
		if err != nil {
			t.Fatal(err)
		}
		workload, err := resolver.Workload(context.Background(), reference(objects[0]))
		if value == nil {
			if !errors.Is(err, ErrInvalidInput) || workload != nil {
				t.Fatalf("nil capture = %+v, %v", workload, err)
			}
		} else if err != nil || !reflect.DeepEqual(workload.Object(), objects[0]) || !reflect.DeepEqual(workload.Pods(), capture.Pods) {
			t.Fatalf("capture = %+v, %v", workload, err)
		}
	}
}

func TestOwnerMetadataRetainsCanonicalObservationBeforeUIDCheck(t *testing.T) {
	root := indexTestObject("root", "", indexBundleGVK, nil)
	base := newOwnerRecordReader(root)
	reader := newOwnerMetadataTestReader(base, root)
	c := newCollection(reader)
	c.metadataOnly = true
	ref := reference(root)
	wrong := ref
	wrong.Namespace, wrong.UID = "team", "wrong"
	canonicalWrong := wrong
	canonicalWrong.Namespace = ""
	want := &ownership.Error{Kind: ownership.ErrUIDMismatch, Expected: canonicalWrong, Actual: ref}
	readCtx, cancelRead := context.WithCancel(context.Background())
	defer cancelRead()
	reader.onGet = func(ObjectRef) { cancelRead() }
	if _, err := c.lookup(readCtx, wrong); !reflect.DeepEqual(err, want) {
		t.Fatalf("wrong UID after cancellation = %#v, want %#v", err, want)
	}
	reader.onGet = nil
	if len(c.lookups) != 0 || len(c.directOwners) != 1 {
		t.Fatal("wrong UID entered successful cache or lost observed metadata")
	}
	var shared *ownership.Object
	for _, namespace := range []string{"team", "other", "", "team"} {
		incoming := ref
		incoming.Namespace = namespace
		result, err := c.lookup(context.Background(), incoming)
		if err != nil || result.Ref != ref || (shared != nil && shared != c.lookups[incoming]) {
			t.Fatalf("canonical lookup = %+v, %v", result, err)
		}
		shared = c.lookups[incoming]
		result.Ref.Name = "changed returned scalar"
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.lookup(ctx, ref); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if reader.calls[canonicalWrong] != 1 || len(reader.calls) != 1 || len(base.gets) != 0 || len(c.lookups) != 3 {
		t.Fatal("canonical hit refetched metadata or fell back to full Get")
	}
}

func TestOwnerMetadataDetachesBeforeReaderCallbacks(t *testing.T) {
	for _, callback := range []string{"mapping", "metadata"} {
		t.Run(callback, func(t *testing.T) {
			root := indexTestObject("root", "team", indexBundleGVK, nil)
			child := indexTestObject("child", "team", indexWorkerGVK, root)
			owners := child.GetOwnerReferences()
			block := true
			owners[0].BlockOwnerDeletion = &block
			child.SetOwnerReferences(owners)
			base := newOwnerRecordReader(child, root)
			reader := newOwnerMetadataTestReader(base, child, root)
			c := newCollection(reader)
			c.metadataOnly = true
			ref := reference(child)
			want, err := c.lookup(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			want.Owners = []metav1.OwnerReference{*want.Owners[0].DeepCopy()}
			metadata := reader.metadata[objectKey{ref.GVK, ref.Namespace, ref.Name}]
			mutated := false
			mutate := func() {
				mutated = true
				metadata.Name, metadata.Namespace, metadata.UID, metadata.ResourceVersion = "changed", "changed", "changed", "changed"
				owner := &metadata.OwnerReferences[0]
				owner.APIVersion, owner.Kind, owner.Name, owner.UID = "changed", "changed", "changed", "changed"
				*owner.Controller, *owner.BlockOwnerDeletion = false, false
			}
			if callback == "mapping" {
				base.onMapping = func(schema.GroupVersionKind) { mutate() }
			} else {
				reader.onGet = func(ObjectRef) { mutate() }
			}
			if _, err := c.lookup(context.Background(), reference(root)); err != nil {
				t.Fatal(err)
			}
			actual, err := c.lookup(context.Background(), ref)
			if !mutated || err != nil || !reflect.DeepEqual(actual, want) || c.directIdentities[ref.UID].resourceVersion != "1" {
				t.Fatalf("reader reuse changed retained ownership: %+v, %v", actual, err)
			}
		})
	}
}

func TestOwnerMetadataAliasValidation(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, conflict := range []string{"none", "resource_version", "owners", "nullable_bool"} {
			t.Run(map[bool]string{false: "metadata", true: "full"}[full]+"/"+conflict, func(t *testing.T) {
				root := indexTestObject("root", "team", indexBundleGVK, nil)
				root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "observer", UID: "observer"}})
				alias := root.DeepCopy()
				alias.SetAPIVersion(indexBundleGVK.Group + "/v2")
				base := newOwnerRecordReader(root, alias)
				reader := newOwnerMetadataTestReader(base, root, alias)
				ref := reference(alias)
				metadata := reader.metadata[objectKey{ref.GVK, ref.Namespace, ref.Name}]
				switch conflict {
				case "resource_version":
					metadata.ResourceVersion = "2"
				case "owners":
					metadata.OwnerReferences[0].Name = "different"
				case "nullable_bool":
					value := false
					metadata.OwnerReferences[0].Controller = &value
				}
				c := newCollection(reader)
				c.metadataOnly = !full
				if _, err := c.lookup(context.Background(), reference(root)); err != nil {
					t.Fatal(err)
				}
				c.metadataOnly = true
				actual, err := c.lookup(context.Background(), ref)
				if conflict != "none" {
					if !errors.Is(err, ErrStaleInput) || c.lookups[ref] != nil {
						t.Fatalf("conflicting alias = %+v, %v", actual, err)
					}
					return
				}
				if err != nil || actual.Ref != ref || c.lookups[reference(root)] == c.lookups[ref] {
					t.Fatalf("served alias = %+v, %v", actual, err)
				}
				if full && !reflect.DeepEqual(c.objects[objectKey{indexBundleGVK, "team", "root"}], root) {
					t.Fatal("typed alias replaced initial full body")
				}
			})
		}
	}
}

func TestOwnerMetadataExcludesCorePodsAndRejectsPodUIDAliases(t *testing.T) {
	for _, gvk := range []schema.GroupVersionKind{podGVK, {Group: "custom.test", Version: "v1", Kind: "Pod"}} {
		for _, listed := range []bool{false, true} {
			pod := indexTestPod("owner", "team", nil)
			object, err := podObject(pod)
			if err != nil {
				t.Fatal(err)
			}
			object.SetGroupVersionKind(gvk)
			base := newOwnerRecordReader(object)
			reader := newOwnerMetadataTestReader(base, object)
			c := newCollection(reader)
			c.metadataOnly = true
			if listed {
				c.podUIDs[pod.UID] = pod
				c.pods[objectKey{podGVK, pod.Namespace, pod.Name}] = pod
			}
			_, err = c.lookup(context.Background(), reference(object))
			if listed && gvk != podGVK {
				if !errors.Is(err, ErrStaleInput) {
					t.Fatalf("Pod UID collision = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if gvk == podGVK && len(reader.calls) != 0 {
				t.Fatal("core Pod used metadata")
			}
			if gvk != podGVK && (reader.calls[reference(object)] != 1 || len(base.gets) != 0) {
				t.Fatal("custom-group Pod did not use metadata")
			}
		}
	}
}
