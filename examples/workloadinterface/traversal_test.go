// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func ExamplePodSource_traverse() {
	ctx := context.Background()
	dataset, _ := inspectableJobSetDataset()
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
	fmt.Printf("workload=%s/%s pods=%v\n", observed.Root().GVK.Kind, observed.Root().Name, podNames(observed.Pods()))

	desired, err := observed.DesiredTree(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("phases=%v condition=%s=%s\n", desired.Status.Phases, desired.RootStatus.Conditions[0].Type, *desired.RootStatus.Conditions[0].Status)
	for component, instance := range desired.Instances() {
		if !component.HasPodDefinition {
			continue
		}
		id := ""
		if instance.InstanceKey != nil {
			id = *instance.InstanceKey
		}
		componentScope, err := observed.Component(component.Name)
		if err != nil {
			panic(err)
		}
		instanceScope, err := componentScope.Instance(id)
		if err != nil {
			panic(err)
		}
		pods, err := instanceScope.Pods()
		if err != nil {
			panic(err)
		}
		fmt.Printf("component=%s instance=%s desired=%d pods=%v\n",
			component.Name, id, *instance.Scale.Replicas, podNames(pods))
	}
	for _, member := range observed.Pods() {
		fmt.Printf("pod=%s phase=%s\n", member.Name, member.Status.Phase)
	}
	// Output:
	// workload=JobSet/training-set pods=[evaluation-0-pod training-0-pod training-1-pod]
	// phases=[Running] condition=Completed=False
	// component=replicatedjob instance=evaluation desired=1 pods=[evaluation-0-pod]
	// component=replicatedjob instance=training desired=2 pods=[training-0-pod training-1-pod]
	// pod=evaluation-0-pod phase=Running
	// pod=training-0-pod phase=Running
	// pod=training-1-pod phase=Running
}

func ExamplePodSource_groupIDs() {
	ctx := context.Background()
	dataset, root := leaderWorkerDataset()
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
	groups, err := observed.Component("group")
	if err != nil {
		panic(err)
	}
	// This catalog component has one anonymous instance and two observed groups.
	instance, err := groups.Instance("")
	if err != nil {
		panic(err)
	}
	for _, groupID := range []string{"0", "1"} {
		group, err := instance.Replica(groupID)
		if err != nil {
			panic(err)
		}
		members, err := group.Pods()
		if err != nil {
			panic(err)
		}
		fmt.Printf("group=%s pods=%v\n", groupID, podNames(members))
		for _, name := range []string{"leader", "worker"} {
			component, err := group.Component(name)
			if err != nil {
				panic(err)
			}
			pods, err := component.Pods()
			if err != nil {
				panic(err)
			}
			fmt.Printf("  component=%s pods=%v\n", name, podNames(pods))
		}
	}
	// Output:
	// group=0 pods=[inference-0 inference-0-1]
	//   component=leader pods=[inference-0]
	//   component=worker pods=[inference-0-1]
	// group=1 pods=[inference-1 inference-1-1]
	//   component=leader pods=[inference-1]
	//   component=worker pods=[inference-1-1]
}

func ExampleDesiredInspector_namedMutation() {
	ctx := context.Background()
	dataset, root := inspectableJobSetDataset()
	discoverer, err := karta.NewDiscoverer(&datasetReader{dataset: dataset}, discovery.Options{})
	if err != nil {
		panic(err)
	}
	observed, err := discoverer.Workload(ctx, objectRef(root))
	if err != nil {
		panic(err)
	}
	editor, err := karta.New(observed.Definition(), observed.Object())
	if err != nil {
		panic(err)
	}
	if err := editor.UpdatePodTemplate(ctx, "replicatedjob", karta.Patch{
		"spec": map[string]any{"schedulerName": "example-scheduler"},
	}, karta.WithInstances("training")); err != nil {
		panic(err)
	}
	edited, err := editor.Tree(ctx)
	if err != nil {
		panic(err)
	}
	for _, instance := range edited.Children[0].Instances {
		fmt.Printf("instance=%s scheduler=%q\n", *instance.InstanceKey,
			instance.ExtractedInstance.PodTemplateSpec.Spec.SchedulerName)
	}
	before, err := observed.DesiredTree(ctx)
	if err != nil {
		panic(err)
	}
	for _, instance := range before.Children[0].Instances {
		fmt.Printf("captured instance=%s scheduler=%q\n", *instance.InstanceKey,
			instance.ExtractedInstance.PodTemplateSpec.Spec.SchedulerName)
	}
	// Output:
	// instance=evaluation scheduler=""
	// instance=training scheduler="example-scheduler"
	// captured instance=evaluation scheduler=""
	// captured instance=training scheduler=""
}

func ExamplePodSource_schedulingGroups() {
	ctx := context.Background()
	dataset, root := inspectableJobSetDataset()
	discoverer, err := karta.NewDiscoverer(&datasetReader{dataset: dataset}, discovery.Options{})
	if err != nil {
		panic(err)
	}
	observed, err := discoverer.Workload(ctx, objectRef(root))
	if err != nil {
		panic(err)
	}
	groups, err := workloadinterface.SchedulingGroups(ctx, observed)
	if err != nil {
		panic(err)
	}
	for _, group := range groups {
		key, err := json.Marshal([]any{group.Name, group.Keys})
		if err != nil {
			panic(err)
		}
		fmt.Printf("gang=%s pods=%v\n", key, podNames(group.Pods))
	}
	// Output:
	// gang=["job",["evaluation"]] pods=[evaluation-0-pod]
	// gang=["job",["training"]] pods=[training-0-pod training-1-pod]
}

func ExampleDesiredInspector_existingDiscovery() {
	ctx := context.Background()
	dataset, root := inspectableJobSetDataset()
	reader := &datasetReader{dataset: dataset}
	// An existing controller already knows this root and its member Pods.
	rootRef, existingPods := objectRef(root), dataset.Pods
	object, err := reader.Get(ctx, rootRef)
	if err != nil {
		panic(err)
	}
	definition, err := catalog.Get(rootRef.GVK)
	if err != nil {
		panic(err)
	}
	workload, err := karta.New(definition, object)
	if err != nil {
		panic(err)
	}
	summary, err := workloadinterface.SummarizeDesired(ctx, workload)
	if err != nil {
		panic(err)
	}
	fmt.Printf("workload=%s phases=%v declared-instances=%d\n", object.GetName(), summary.Phases, len(summary.Instances))
	fmt.Printf("existing pods=%v full-object-reads=%d\n", podNames(existingPods), reader.reads)
	// Output:
	// workload=training-set phases=[Running] declared-instances=3
	// existing pods=[evaluation-0-pod training-0-pod training-1-pod] full-object-reads=1
}

func podNames(pods []*corev1.Pod) []string {
	names := make([]string, len(pods))
	for i, pod := range pods {
		names[i] = pod.Name
	}
	slices.Sort(names)
	return names
}

func inspectableJobSetDataset() (discovery.Dataset, *unstructured.Unstructured) {
	dataset, root := jobSetDataset()
	jobs := []any{}
	for _, entry := range []struct {
		name     string
		replicas int64
	}{{"training", 2}, {"evaluation", 1}} {
		jobs = append(jobs, map[string]any{
			"name": entry.name, "replicas": entry.replicas,
			"template": map[string]any{"spec": map[string]any{
				"parallelism": int64(1), "template": map[string]any{"spec": map[string]any{
					"restartPolicy": "Never", "containers": []any{map[string]any{"name": "trainer", "image": "example/trainer:v1"}},
				}},
			}},
		})
	}
	root.Object["spec"] = map[string]any{"replicatedJobs": jobs}
	root.Object["status"] = map[string]any{
		"conditions":           []any{map[string]any{"type": "Completed", "status": "False"}},
		"replicatedJobsStatus": []any{map[string]any{"name": "training", "active": int64(2), "ready": int64(2), "failed": int64(0)}},
	}
	return dataset, root
}

func leaderWorkerDataset() (discovery.Dataset, *unstructured.Unstructured) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "leaderworkerset.x-k8s.io/v1", "kind": "LeaderWorkerSet",
		"metadata": map[string]any{"name": "inference", "namespace": "example", "uid": "lws-uid", "resourceVersion": "1"},
		"spec":     map[string]any{"replicas": int64(2), "leaderWorkerTemplate": map[string]any{"size": int64(2)}},
	}}
	dataset := jobDataset(root, nil)
	dataset.Mappings[root.GroupVersionKind()].Resource.Resource = "leaderworkersets"
	for _, group := range []string{"0", "1"} {
		for _, worker := range []string{"0", "1"} {
			name := "inference-" + group
			if worker != "0" {
				name += "-" + worker
			}
			pod := ownedPod(name, root, corev1.PodRunning)
			pod.Labels = map[string]string{
				"leaderworkerset.sigs.k8s.io/name":         "inference",
				"leaderworkerset.sigs.k8s.io/group-index":  group,
				"leaderworkerset.sigs.k8s.io/worker-index": worker,
			}
			if worker != "0" {
				pod.Annotations = map[string]string{"leaderworkerset.sigs.k8s.io/leader-name": "inference-" + group}
			}
			dataset.Pods = append(dataset.Pods, pod)
		}
	}
	return dataset, root
}
