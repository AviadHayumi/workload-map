// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package catalog

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

var _ = Describe("Kueue Workload status", func() {
	DescribeTable("reads admission and readiness without inferring execution success",
		func(ctx SpecContext, conditions []map[string]any, want v1alpha1.ResourceStatus) {
			karta, err := Get(schema.GroupVersionKind{Group: "kueue.x-k8s.io", Version: "v1beta2", Kind: "Workload"})
			Expect(err).NotTo(HaveOccurred())
			object := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "kueue.x-k8s.io/v1beta2",
				"kind":       "Workload",
			}}
			if conditions != nil {
				values := make([]any, len(conditions))
				for i, condition := range conditions {
					values[i] = condition
				}
				Expect(unstructured.SetNestedSlice(object.Object, values, "status", "conditions")).To(Succeed())
			}
			root, err := resource.NewComponentFactoryFromObject(karta, object).GetRootComponent()
			Expect(err).NotTo(HaveOccurred())
			status, err := root.GetStatus(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.MatchedStatuses).To(Equal([]v1alpha1.ResourceStatus{want}))
		},
		Entry("missing conditions", nil, v1alpha1.UndefinedStatus),
		Entry("waiting for admission", []map[string]any{
			{"type": "Admitted", "status": "False"},
		}, v1alpha1.InitializingStatus),
		Entry("unknown admission", []map[string]any{
			{"type": "Admitted", "status": "Unknown"},
		}, v1alpha1.UndefinedStatus),
		Entry("admitted without readiness reporting", []map[string]any{
			{"type": "Admitted", "status": "True"},
		}, v1alpha1.UndefinedStatus),
		Entry("admitted before Pods are ready", []map[string]any{
			{"type": "Admitted", "status": "True"},
			{"type": "PodsReady", "status": "False"},
		}, v1alpha1.UndefinedStatus),
		Entry("admitted and ready", []map[string]any{
			{"type": "Admitted", "status": "True"},
			{"type": "PodsReady", "status": "True"},
		}, v1alpha1.RunningStatus),
		Entry("readiness retained after admission is withdrawn", []map[string]any{
			{"type": "Admitted", "status": "False"},
			{"type": "PodsReady", "status": "True"},
		}, v1alpha1.InitializingStatus),
		Entry("readiness retained during eviction", []map[string]any{
			{"type": "Admitted", "status": "True"},
			{"type": "PodsReady", "status": "True"},
			{"type": "Evicted", "status": "True"},
		}, v1alpha1.UndefinedStatus),
		Entry("readiness retained while waiting for replacement Pods", []map[string]any{
			{"type": "Admitted", "status": "True"},
			{"type": "PodsReady", "status": "True"},
			{"type": "WaitingForReplacementPods", "status": "True"},
		}, v1alpha1.UndefinedStatus),
		Entry("finished mixed Pod group with stale readiness", []map[string]any{
			{"type": "Admitted", "status": "True"},
			{"type": "PodsReady", "status": "True"},
			{"type": "Finished", "status": "True", "reason": "Succeeded", "message": "Pods succeeded: 1/3."},
		}, v1alpha1.UndefinedStatus),
		Entry("finished after quota was released", []map[string]any{
			{"type": "Admitted", "status": "False"},
			{"type": "Finished", "status": "True", "reason": "Failed"},
		}, v1alpha1.UndefinedStatus),
		Entry("recovered after eviction and replacement", []map[string]any{
			{"type": "Admitted", "status": "True"},
			{"type": "PodsReady", "status": "True"},
			{"type": "Evicted", "status": "False"},
			{"type": "WaitingForReplacementPods", "status": "False"},
			{"type": "Finished", "status": "False"},
		}, v1alpha1.RunningStatus),
	)
})
