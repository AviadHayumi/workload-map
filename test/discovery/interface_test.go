// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build live_discovery

package discovery_test

import (
	"context"
	"reflect"
	"testing"

	example "github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// assertInterfaceConsumers runs the README adapters on real observations. The
// caller checks membership against an independent Kubernetes UID oracle.
func assertInterfaceConsumers(t *testing.T, ctx context.Context, observed *sdk.Workload, expectedPods int) {
	t.Helper()
	counts := example.CountPods(observed)
	if counts.Total != expectedPods {
		t.Fatalf("README Pod count = %d, want %d", counts.Total, expectedPods)
	}
	if counts.Pending+counts.Running+counts.Succeeded+counts.Failed+counts.Unknown != counts.Total {
		t.Fatalf("phase counters do not partition Pods: %+v", counts)
	}
	if observed.Root().Name == "sdk-finite" && counts.Succeeded != 1 {
		t.Fatalf("completed Job missing from README summary: %+v", counts)
	}
	if observed.Generic() {
		return
	}
	before := observed.Object()
	offline, err := karta.New(observed.Definition(), before)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := example.SummarizeDesired(ctx, offline)
	if err != nil {
		t.Fatal(err)
	}
	// Count template locations, not desired or observed Pod replicas.
	expectedTemplates := map[string]int{
		"JobSet": 2, "LeaderWorkerSet": 2, "Deployment": 1, "Pod": 1, "Job": 1,
	}
	templates := 0
	for _, instance := range summary.Instances {
		extracted := instance.Extracted
		if extracted != nil && (extracted.PodTemplateSpec != nil || extracted.PodSpec != nil || extracted.FragmentedPodSpec != nil) {
			templates++
		}
	}
	if templates != expectedTemplates[observed.Root().GVK.Kind] {
		t.Fatalf("README desired summary: %d template locations for %s, want %d", templates, observed.Root().GVK.Kind, expectedTemplates[observed.Root().GVK.Kind])
	}
	if observed.Root().GVK.Kind == "Deployment" {
		if err := offline.UpdatePodTemplate(ctx, "deployment", karta.PodPatch{Labels: map[string]string{"interface-example": "local-edit"}}); err != nil {
			t.Fatal(err)
		}
		edited, err := offline.Object()
		if err != nil {
			t.Fatal(err)
		}
		value, found, err := unstructured.NestedString(edited.(*unstructured.Unstructured).Object, "spec", "template", "metadata", "labels", "interface-example")
		if err != nil || !found || value != "local-edit" {
			t.Fatalf("local template patch missing: %q %v %v", value, found, err)
		}
		if !reflect.DeepEqual(before, observed.Object()) || example.CountPods(observed) != counts {
			t.Fatal("offline edit changed the captured live observation")
		}
	}
	t.Logf("README consumers: %+v, %d desired template locations", counts, templates)
}
