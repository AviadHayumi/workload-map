// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface_test

import (
	"context"
	"fmt"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func ExamplePodSource_ownershipPath() {
	ctx := context.Background()
	dataset, _ := jobSetDataset()
	sdk, err := karta.NewDiscoverer(&datasetReader{dataset: dataset}, discovery.Options{MaxDepth: 8})
	if err != nil {
		panic(err)
	}
	path, err := sdk.OwnershipPath(ctx, discovery.PodReference(dataset.Pods[0]))
	if err != nil {
		panic(err)
	}
	for _, object := range path {
		fmt.Printf("%s/%s\n", object.GVK.Kind, object.Name)
	}
	// Output:
	// Pod/training-0-pod
	// Job/training-0
	// JobSet/training-set
}

func ExamplePodSource_memberships() {
	ctx := context.Background()
	dataset, _ := jobSetDataset()
	sdk, err := karta.NewDiscoverer(&datasetReader{dataset: dataset}, discovery.Options{})
	if err != nil {
		panic(err)
	}
	observed, err := sdk.WorkloadForPod(ctx, discovery.PodReference(dataset.Pods[0]))
	if err != nil {
		panic(err)
	}
	memberships := observed.PodMemberships()
	for _, pod := range observed.Pods() {
		membership := memberships[pod.UID]
		if len(membership.Issues) != 0 {
			panic("fixture attribution is incomplete")
		}
		fmt.Printf("pod=%s component=%s\n", pod.Name, membership.Component)
		for _, coordinate := range membership.Path {
			if coordinate.Component == membership.Component && coordinate.Instance != nil && coordinate.Replica != nil {
				fmt.Printf("  instance=%s replica=%s\n", *coordinate.Instance, *coordinate.Replica)
			}
		}
	}
	// Output:
	// pod=evaluation-0-pod component=replicatedjob
	//   instance=evaluation replica=0
	// pod=training-0-pod component=replicatedjob
	//   instance=training replica=0
	// pod=training-1-pod component=replicatedjob
	//   instance=training replica=1
}
