// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface_test

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func ExampleSummarizeDesired() {
	ctx := context.Background()
	input := exampleJob()
	definition, err := catalog.Get(input.GroupVersionKind())
	if err != nil {
		panic(err)
	}
	workload, err := karta.New(definition, input)
	if err != nil {
		panic(err)
	}
	summary, err := workloadinterface.SummarizeDesired(ctx, workload)
	if err != nil {
		panic(err)
	}
	root := summary.Instances[0]
	fmt.Printf("%s: replicas=%d image=%s\n", root.Component,
		*root.Scale.Replicas, root.Extracted.PodTemplateSpec.Spec.Containers[0].Image)
	fmt.Printf("phase=%s condition=%s\n", summary.Phases[0], summary.Status.Conditions[0].Type)
	// Output:
	// job: replicas=2 image=example/trainer:v1
	// phase=Completed condition=Complete
}

func ExampleDesiredInspector_mutation() {
	ctx := context.Background()
	input := exampleJob()
	definition, err := catalog.Get(input.GroupVersionKind())
	if err != nil {
		panic(err)
	}
	workload, err := karta.New(definition, input)
	if err != nil {
		panic(err)
	}
	if err := workload.UpdatePodTemplate(ctx, "job", karta.Patch{
		"spec": map[string]any{"schedulerName": "example-scheduler"},
	}); err != nil {
		panic(err)
	}
	summary, err := workloadinterface.SummarizeDesired(ctx, workload)
	if err != nil {
		panic(err)
	}
	fmt.Printf("input scheduler: %q\n", input.Spec.Template.Spec.SchedulerName)
	fmt.Printf("desired scheduler: %q\n", summary.Instances[0].Extracted.PodTemplateSpec.Spec.SchedulerName)
	// Object returns a copy ready for the caller's explicit create or update.
	object, err := workload.Object()
	if err != nil {
		panic(err)
	}
	fmt.Printf("object: %s/%s\n", object.GetNamespace(), object.GetName())
	// Output:
	// input scheduler: ""
	// desired scheduler: "example-scheduler"
	// object: example/training
}

func ExampleCountPods() {
	ctx := context.Background()
	root := jobObject()
	pods := []*corev1.Pod{
		ownedPod("running", root, corev1.PodRunning),
		ownedPod("completed", root, corev1.PodSucceeded),
		ownedPod("waiting", root, corev1.PodPending),
	}
	pods[0].DeletionTimestamp = ptr.To(metav1.Now())
	pods[2].Spec.NodeName = ""
	index := discovery.NewIndex()
	if _, err := index.Replace(ctx, jobDataset(root, pods)); err != nil {
		panic(err)
	}
	discoverer, err := karta.NewDiscoverer(index, discovery.Options{})
	if err != nil {
		panic(err)
	}
	observed, err := discoverer.Workload(ctx, objectRef(root))
	if err != nil {
		panic(err)
	}
	counts := workloadinterface.CountPods(observed)
	fmt.Printf("total=%d running=%d completed=%d pending=%d\n",
		counts.Total, counts.Running, counts.Succeeded, counts.Pending)
	fmt.Printf("terminating=%d unscheduled=%d\n", counts.Terminating, counts.Unscheduled)
	// Output:
	// total=3 running=1 completed=1 pending=1
	// terminating=1 unscheduled=1
}

func ExamplePodSource_scopes() {
	ctx := context.Background()
	dataset, root := jobSetDataset()
	index := discovery.NewIndex()
	if _, err := index.Replace(ctx, dataset); err != nil {
		panic(err)
	}
	discoverer, err := karta.NewDiscoverer(index, discovery.Options{})
	if err != nil {
		panic(err)
	}
	observed, err := discoverer.Workload(ctx, objectRef(root))
	if err != nil {
		panic(err)
	}
	jobs, err := observed.Component("replicatedjob")
	if err != nil {
		panic(err)
	}
	training, err := jobs.Instance("training")
	if err != nil {
		panic(err)
	}
	first, err := training.Replica("0")
	if err != nil {
		panic(err)
	}
	pods, err := first.Pods()
	if err != nil {
		panic(err)
	}
	fmt.Printf("workload=%d training/0=%d\n", len(observed.Pods()), len(pods))
	// Output:
	// workload=3 training/0=1
}

func exampleJob() *batchv1.Job {
	return &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "example", Name: "training", UID: "job-uid", ResourceVersion: "1"},
		Spec: batchv1.JobSpec{Parallelism: ptr.To(int32(2)), Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{Name: "trainer", Image: "example/trainer:v1"}}},
		}},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
}

func jobObject() *unstructured.Unstructured {
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(exampleJob())
	if err != nil {
		panic(err)
	}
	return &unstructured.Unstructured{Object: data}
}

func objectRef(object *unstructured.Unstructured) discovery.ObjectRef {
	return discovery.ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
}

func ownedPod(name string, owner *unstructured.Unstructured, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: owner.GetNamespace(), Name: name, UID: types.UID(name), ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(),
				Name: owner.GetName(), UID: owner.GetUID(), Controller: ptr.To(true)}}},
		Spec: corev1.PodSpec{NodeName: "example-node"}, Status: corev1.PodStatus{Phase: phase},
	}
}

func jobDataset(root *unstructured.Unstructured, pods []*corev1.Pod) discovery.Dataset {
	return discovery.Dataset{Namespace: root.GetNamespace(), Objects: []*unstructured.Unstructured{root}, Pods: pods,
		Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
			{Version: "v1", Kind: "Pod"}: {
				GroupVersionKind: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
				Resource:         schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Scope: meta.RESTScopeNamespace,
			},
			root.GroupVersionKind(): {
				GroupVersionKind: root.GroupVersionKind(),
				Resource:         schema.GroupVersionResource{Group: root.GroupVersionKind().Group, Version: root.GroupVersionKind().Version, Resource: "jobs"},
				Scope:            meta.RESTScopeNamespace,
			},
		},
	}
}

func jobSetDataset() (discovery.Dataset, *unstructured.Unstructured) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "jobset.x-k8s.io/v1alpha2", "kind": "JobSet",
		"metadata": map[string]any{"name": "training-set", "namespace": "example", "uid": "jobset-uid", "resourceVersion": "1"},
		"spec":     map[string]any{"replicatedJobs": []any{map[string]any{"name": "training"}, map[string]any{"name": "evaluation"}}},
	}}
	dataset := jobDataset(root, nil)
	rootMapping := dataset.Mappings[root.GroupVersionKind()]
	rootMapping.Resource.Resource = "jobsets"
	jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	dataset.Mappings[jobGVK] = &meta.RESTMapping{GroupVersionKind: jobGVK,
		Resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, Scope: meta.RESTScopeNamespace}
	for _, member := range []struct{ instance, replica string }{{"training", "0"}, {"training", "1"}, {"evaluation", "0"}} {
		job := jobObject()
		job.SetName(member.instance + "-" + member.replica)
		job.SetUID(types.UID(job.GetName()))
		job.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: root.GetAPIVersion(), Kind: root.GetKind(),
			Name: root.GetName(), UID: root.GetUID(), Controller: ptr.To(true)}})
		pod := ownedPod(job.GetName()+"-pod", job, corev1.PodRunning)
		pod.Labels = map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": member.instance, "jobset.sigs.k8s.io/job-index": member.replica}
		dataset.Objects = append(dataset.Objects, job)
		dataset.Pods = append(dataset.Pods, pod)
	}
	return dataset, root
}
