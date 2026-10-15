// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/pkg/tree"
)

type desiredStub struct {
	tree *tree.WorkloadTree
	err  error
}

func (s desiredStub) Tree(context.Context) (*tree.WorkloadTree, error) { return s.tree, s.err }

type podSource []*corev1.Pod

func (p podSource) Pods() []*corev1.Pod { return p }

type datasetReader struct {
	dataset discovery.Dataset
	reads   int
}

func (r *datasetReader) Mapping(_ context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	r.reads++
	mapping, ok := r.dataset.Mappings[gvk]
	if !ok {
		return nil, fmt.Errorf("missing mapping: %s", gvk)
	}
	return mapping, nil
}

func (r *datasetReader) Get(_ context.Context, ref discovery.ObjectRef) (*unstructured.Unstructured, error) {
	r.reads++
	for _, object := range r.dataset.Objects {
		if objectRef(object) == ref {
			return object.DeepCopy(), nil
		}
	}
	for _, pod := range r.dataset.Pods {
		if ref.GVK == (schema.GroupVersionKind{Version: "v1", Kind: "Pod"}) &&
			pod.Name == ref.Name && pod.Namespace == ref.Namespace && pod.UID == ref.UID {
			data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
			return &unstructured.Unstructured{Object: data}, err
		}
	}
	return nil, fmt.Errorf("missing object: %s", ref.Name)
}

func (r *datasetReader) ListPods(_ context.Context, namespace string) (*corev1.PodList, error) {
	r.reads++
	list := &corev1.PodList{}
	for _, pod := range r.dataset.Pods {
		if namespace == "" || pod.Namespace == namespace {
			list.Items = append(list.Items, *pod.DeepCopy())
		}
	}
	return list, nil
}

func TestSummarizeDesiredPreservesPublicData(t *testing.T) {
	status := &resource.Status{Phase: ptr.To("Ready"), Conditions: []resource.Condition{{Type: "Available", Status: ptr.To("True")}}}
	scale := &resource.Scale{Replicas: ptr.To(int32(2))}
	rootSpec := &resource.ExtractedInstance{PodTemplateSpec: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{SchedulerName: "root"}}, Scale: scale}
	splitSpec := &resource.ExtractedInstance{PodSpec: &corev1.PodSpec{SchedulerName: "split"}, Metadata: &metav1.ObjectMeta{Labels: map[string]string{"role": "worker"}}}
	fragmentedSpec := &resource.ExtractedInstance{FragmentedPodSpec: &resource.FragmentedPodSpec{Image: "example/worker:v1"}}
	children := []tree.ComponentNode{{Name: "split", Instances: []tree.InstanceNode{{InstanceKey: ptr.To("worker"), ExtractedInstance: splitSpec}}},
		{Name: "fragmented", Instances: []tree.InstanceNode{{ExtractedInstance: fragmentedSpec}}}}
	inspector := desiredStub{tree: &tree.WorkloadTree{
		Root:       &tree.ComponentNode{Name: "root", Instances: []tree.InstanceNode{{Scale: scale, ExtractedInstance: rootSpec, Children: children}}},
		RootStatus: status, Status: &tree.WorkloadStatus{Phases: []string{"Running"}}, Children: children,
	}}
	summary, err := workloadinterface.SummarizeDesired(t.Context(), inspector)
	if err != nil {
		t.Fatal(err)
	}
	want := &workloadinterface.DesiredSummary{Status: status, Phases: []string{"Running"}, Instances: []workloadinterface.DesiredInstance{
		{Component: "root", Path: []string{"root"}, Scale: scale, Extracted: rootSpec},
		{Component: "split", Path: []string{"root", "split"}, InstanceKey: ptr.To("worker"), Extracted: splitSpec},
		{Component: "fragmented", Path: []string{"root", "fragmented"}, Extracted: fragmentedSpec},
	}}
	if diff := cmp.Diff(want, summary); diff != "" {
		t.Errorf("SummarizeDesired() mismatch (-want +got):\n%s", diff)
	}
}

func TestSummarizeDesiredErrors(t *testing.T) {
	inspectErr := errors.New("inspection failed")
	if _, err := workloadinterface.SummarizeDesired(t.Context(), desiredStub{err: inspectErr}); !errors.Is(err, inspectErr) {
		t.Errorf("SummarizeDesired() = %v, want inspection error", err)
	}
	for _, missing := range []*tree.WorkloadTree{nil, {}} {
		if _, err := workloadinterface.SummarizeDesired(t.Context(), desiredStub{tree: missing}); err == nil {
			t.Error("SummarizeDesired() accepted a missing root")
		}
	}
}

func TestCountPodsConsumerFake(t *testing.T) {
	for _, tc := range []struct {
		name string
		pods podSource
		want workloadinterface.PodCounts
	}{
		{name: "empty"},
		{name: "all_states", pods: podSource{
			{Status: corev1.PodStatus{Phase: corev1.PodPending}},
			{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: ptr.To(metav1.Now())}, Spec: corev1.PodSpec{NodeName: "example-node"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
			{Spec: corev1.PodSpec{NodeName: "example-node"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
			{Spec: corev1.PodSpec{NodeName: "example-node"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
			{Spec: corev1.PodSpec{NodeName: "example-node"}},
		}, want: workloadinterface.PodCounts{Total: 5, Pending: 1, Running: 1, Succeeded: 1, Failed: 1, Unknown: 1, Terminating: 1, Unscheduled: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workloadinterface.CountPods(tc.pods); got != tc.want {
				t.Errorf("CountPods() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCountPodsThroughInjectedReader(t *testing.T) {
	for _, tc := range []struct {
		name        string
		generic     bool
		brokenScale bool
		zeroPods    bool
	}{
		{name: "catalog"}, {name: "zero_pods", zeroPods: true},
		{name: "generic", generic: true}, {name: "broken_desired_scale", brokenScale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := jobObject()
			if tc.generic {
				root.SetAPIVersion("example.org/v1")
				root.SetKind("Training")
			}
			if tc.brokenScale {
				if err := unstructured.SetNestedField(root.Object, "invalid", "spec", "parallelism"); err != nil {
					t.Fatal(err)
				}
			}
			var pods []*corev1.Pod
			if !tc.zeroPods {
				pods = []*corev1.Pod{ownedPod("completed", root, corev1.PodSucceeded), ownedPod("pending", root, corev1.PodPending)}
				pods[1].Spec.NodeName = ""
				pods[1].DeletionTimestamp = ptr.To(metav1.Now())
			}
			reader := &datasetReader{dataset: jobDataset(root, pods)}
			discoverer, err := karta.NewDiscoverer(reader, discovery.Options{})
			if err != nil {
				t.Fatal(err)
			}
			observed, err := discoverer.Workload(t.Context(), objectRef(root))
			if err != nil {
				t.Fatal(err)
			}
			reads := reader.reads
			want := workloadinterface.PodCounts{}
			if !tc.zeroPods {
				want = workloadinterface.PodCounts{Total: 2, Pending: 1, Succeeded: 1, Terminating: 1, Unscheduled: 1}
			}
			if got := workloadinterface.CountPods(observed); got != want {
				t.Errorf("CountPods() = %+v, want %+v", got, want)
			}
			if reader.reads != reads {
				t.Error("CountPods() acquired new data")
			}
			if tc.generic {
				if _, err := observed.DesiredTree(t.Context()); !errors.Is(err, discovery.ErrUnsupported) {
					t.Errorf("DesiredTree() = %v, want ErrUnsupported", err)
				}
			} else if tc.brokenScale {
				if _, err := observed.DesiredTree(t.Context()); err == nil {
					t.Error("DesiredTree() accepted invalid scale")
				}
			}
			if !tc.zeroPods {
				fromPod, err := discoverer.WorkloadForPod(t.Context(), discovery.ObjectRef{
					GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, Namespace: pods[0].Namespace, Name: pods[0].Name, UID: pods[0].UID,
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := workloadinterface.CountPods(fromPod); got != want {
					t.Errorf("CountPods(WorkloadForPod()) = %+v, want %+v", got, want)
				}
			}
		})
	}
}

func TestObservedWorkloadSurvivesOfflineMutationAndIndexUpdate(t *testing.T) {
	root := jobObject()
	pod := ownedPod("trainer", root, corev1.PodRunning)
	index := discovery.NewIndex()
	generation, err := index.Replace(t.Context(), jobDataset(root, []*corev1.Pod{pod}))
	if err != nil {
		t.Fatal(err)
	}
	discoverer, err := karta.NewDiscoverer(index, discovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := discoverer.Workload(t.Context(), objectRef(root))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.Get(root.GroupVersionKind())
	if err != nil {
		t.Fatal(err)
	}
	desired, err := karta.New(definition, observed.Object())
	if err != nil {
		t.Fatal(err)
	}
	if err := desired.UpdatePodTemplate(t.Context(), "job", karta.Patch{"spec": map[string]any{"schedulerName": "example-scheduler"}}); err != nil {
		t.Fatal(err)
	}
	summary, err := workloadinterface.SummarizeDesired(t.Context(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary.Instances[0].Extracted.PodTemplateSpec.Spec.SchedulerName; got != "example-scheduler" {
		t.Errorf("desired scheduler = %q, want example-scheduler", got)
	}
	if diff := cmp.Diff(root.Object, observed.Object().Object); diff != "" {
		t.Errorf("offline mutation changed observation (-want +got):\n%s", diff)
	}
	pod.Status.Phase = corev1.PodSucceeded
	pod.ResourceVersion = "2"
	updated, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Apply(t.Context(), generation, discovery.Changes{Upserts: []*unstructured.Unstructured{{Object: updated}}}); err != nil {
		t.Fatal(err)
	}
	fresh, err := discoverer.Workload(t.Context(), objectRef(root))
	if err != nil {
		t.Fatal(err)
	}
	if got := workloadinterface.CountPods(observed); got != (workloadinterface.PodCounts{Total: 1, Running: 1}) {
		t.Errorf("old observation = %+v, want one running Pod", got)
	}
	if got := workloadinterface.CountPods(fresh); got != (workloadinterface.PodCounts{Total: 1, Succeeded: 1}) {
		t.Errorf("fresh observation = %+v, want one succeeded Pod", got)
	}
}
