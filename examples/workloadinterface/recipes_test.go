// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface_test

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func ExamplePodSource_fromPod() {
	ctx := context.Background()
	dataset, root := jobSetDataset()
	discoverer, err := karta.NewDiscoverer(&datasetReader{dataset: dataset}, discovery.Options{})
	if err != nil {
		panic(err)
	}
	pod := dataset.Pods[0]
	observed, err := discoverer.WorkloadForPod(ctx, discovery.ObjectRef{
		GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID,
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("root=%s same UID=%t\n", observed.Root().Name, observed.Root().UID == root.GetUID())
	fmt.Printf("known=%t pods=%d\n", !observed.Generic(), len(observed.Pods()))
	// Output:
	// root=training-set same UID=true
	// known=true pods=3
}

func ExamplePodSource_zeroPods() {
	ctx := context.Background()
	root := jobObject()
	reader := &datasetReader{dataset: jobDataset(root, nil)}
	// Include an explicit root when collecting workloads that have no Pods.
	dataset, err := discovery.Collect(ctx, reader, root.GetNamespace(), objectRef(root))
	if err != nil {
		panic(err)
	}
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
	desired, err := observed.DesiredTree(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("root=%s observed=%d desired=%d\n", observed.Root().Name,
		len(observed.Pods()), *desired.Root.Instances[0].Scale.Replicas)
	// Output:
	// root=training observed=0 desired=2
}

func ExamplePodSource_generic() {
	ctx := context.Background()
	root := jobObject()
	root.SetAPIVersion("example.org/v1")
	root.SetKind("Training")
	pod := ownedPod("trainer", root, corev1.PodRunning)
	discoverer, err := karta.NewDiscoverer(&datasetReader{
		dataset: jobDataset(root, []*corev1.Pod{pod}),
	}, discovery.Options{})
	if err != nil {
		panic(err)
	}
	observed, err := discoverer.Workload(ctx, objectRef(root))
	if err != nil {
		panic(err)
	}
	fmt.Printf("generic=%t definition absent=%t pods=%d\n",
		observed.Generic(), observed.Definition() == nil, len(observed.Pods()))
	component, err := observed.Component("pods")
	if err != nil {
		panic(err)
	}
	instance, err := component.Instance("")
	if err != nil {
		panic(err)
	}
	members, err := instance.Pods()
	if err != nil {
		panic(err)
	}
	_, treeErr := observed.DesiredTree(ctx)
	_, replicaErr := component.Replica("0")
	fmt.Printf("generic members=%d unsupported tree=%t no replica axis=%t\n",
		len(members), errors.Is(treeErr, discovery.ErrUnsupported),
		errors.Is(replicaErr, discovery.ErrNoReplicaAxis))
	// Output:
	// generic=true definition absent=true pods=1
	// generic members=1 unsupported tree=true no replica axis=true
}

func ExampleDesiredInspector_suspendResume() {
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
	fmt.Printf("component=%s suspendable=%t\n",
		workload.Components()[0].Name, workload.Components()[0].Suspendable)
	if err := workload.Suspend(ctx); err != nil {
		panic(err)
	}
	suspended, err := workload.Object()
	if err != nil {
		panic(err)
	}
	if err := workload.Resume(ctx); err != nil {
		panic(err)
	}
	resumed, err := workload.Object()
	if err != nil {
		panic(err)
	}
	isSuspended, _, err := unstructured.NestedBool(suspended.(*unstructured.Unstructured).Object, "spec", "suspend")
	if err != nil {
		panic(err)
	}
	isResumed, _, err := unstructured.NestedBool(resumed.(*unstructured.Unstructured).Object, "spec", "suspend")
	if err != nil {
		panic(err)
	}
	fmt.Printf("suspended=%t resumed=%t input untouched=%t\n",
		isSuspended, !isResumed, input.Spec.Suspend == nil)
	// Output:
	// component=job suspendable=true
	// suspended=true resumed=true input untouched=true
}

func ExamplePodSource_attributionCache() {
	ctx := context.Background()
	root := jobObject()
	pod := ownedPod("worker", root, corev1.PodRunning)
	index := discovery.NewIndex()
	generation, err := index.Replace(ctx, jobDataset(root, []*corev1.Pod{pod}))
	if err != nil {
		panic(err)
	}
	discoverer, err := karta.NewDiscoverer(index, discovery.Options{
		Cache: &discovery.CacheOptions{MaxWorkloads: 1000, MaxPods: 10000},
	})
	if err != nil {
		panic(err)
	}
	before, err := discoverer.Workload(ctx, objectRef(root))
	if err != nil {
		panic(err)
	}
	// A repeated query may reuse attribution; object accessors still return copies.
	repeated, err := discoverer.Workload(ctx, objectRef(root))
	if err != nil {
		panic(err)
	}
	fmt.Printf("new observation=%t pods=%d\n", before != repeated, len(repeated.Pods()))

	// Publish the current object from the caller's ordered Kubernetes feed.
	pod.ResourceVersion = "2"
	pod.Status.Phase = corev1.PodSucceeded
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		panic(err)
	}
	_, err = index.Apply(ctx, generation, discovery.Changes{
		Upserts: []*unstructured.Unstructured{{Object: data}},
	})
	if err != nil {
		panic(err)
	}
	after, err := discoverer.Workload(ctx, objectRef(root))
	if err != nil {
		panic(err)
	}
	fmt.Printf("previous=%s current=%s\n", before.Pods()[0].Status.Phase, after.Pods()[0].Status.Phase)
	// Output:
	// new observation=true pods=1
	// previous=Running current=Succeeded
}
