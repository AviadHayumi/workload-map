// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package attribute

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/catalog"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
)

func entryFor(gvk schema.GroupVersionKind) *registry.Entry {
	karta, err := catalog.Get(gvk)
	Expect(err).NotTo(HaveOccurred())

	r := registry.New()
	r.Set(karta)
	entry, ok := r.EntryFor(gvk.GroupKind())
	Expect(ok).To(BeTrue())
	return entry
}

func jobsetWorkload() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "jobset.x-k8s.io/v1alpha2",
		"kind":       "JobSet",
		"metadata":   map[string]any{"name": "llm", "namespace": "team-a", "uid": "wl-1"},
		"spec": map[string]any{
			"replicatedJobs": []any{
				map[string]any{
					"name": "prefill", "replicas": int64(1),
					"template": map[string]any{"spec": map[string]any{"parallelism": int64(2)}},
				},
				map[string]any{
					"name": "decode", "replicas": int64(1),
					"template": map[string]any{"spec": map[string]any{"parallelism": int64(1)}},
				},
			},
		},
	}}
}

func pod(labels, annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:        "pod-0",
		Namespace:   "team-a",
		Labels:      labels,
		Annotations: annotations,
	}}
}

var _ = Describe("Attribute", func() {
	ctx := context.Background()

	jobsetGVK := schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}
	lwsGVK := schema.GroupVersionKind{Group: "leaderworkerset.x-k8s.io", Version: "v1", Kind: "LeaderWorkerSet"}

	jobsetInstances := map[string][]string{"replicatedjob": {"prefill", "decode"}}

	Context("JobSet with prefill and decode instances", func() {
		It("computes the instance ids from the workload object", func() {
			ids, err := InstanceIDs(ctx, entryFor(jobsetGVK), jobsetWorkload())

			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal(jobsetInstances))
		})

		It("attributes a pod to its component instance from the pod label", func() {
			result := Attribute(ctx,
				pod(map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "decode", "jobset.sigs.k8s.io/job-index": "1"}, nil),
				entryFor(jobsetGVK), jobsetInstances)

			Expect(result).To(Equal(Result{Component: "replicatedjob", Instance: "decode", Replica: "1"}))
		})

		It("degrades to the unknown sentinel when the label matches no declared instance", func() {
			result := Attribute(ctx,
				pod(map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "other"}, nil),
				entryFor(jobsetGVK), jobsetInstances)

			Expect(result.Component).To(Equal("replicatedjob"))
			Expect(result.Instance).To(Equal(collector.SentinelUnknown))
			Expect(result.Reason).To(Equal(collector.ReasonUnknownInstance))
		})

		It("degrades to the unknown sentinel when the instance label is absent", func() {
			result := Attribute(ctx, pod(nil, nil), entryFor(jobsetGVK), jobsetInstances)

			Expect(result.Component).To(Equal("replicatedjob"))
			Expect(result.Instance).To(Equal(collector.SentinelUnknown))
			Expect(result.Reason).To(Equal(collector.ReasonJQError))
		})
	})

	lwsInstances := map[string][]string{"leader": {""}, "worker": {""}}

	Context("LeaderWorkerSet with leader and worker components", func() {
		It("attributes a leader pod and inherits the replica key from the group ancestor", func() {
			result := Attribute(ctx,
				pod(map[string]string{
					"leaderworkerset.sigs.k8s.io/worker-index": "0",
					"leaderworkerset.sigs.k8s.io/group-index":  "1",
				}, nil),
				entryFor(lwsGVK), lwsInstances)

			Expect(result).To(Equal(Result{Component: "leader", Replica: "1"}))
		})

		It("attributes a worker pod by annotation existence", func() {
			result := Attribute(ctx,
				pod(map[string]string{
					"leaderworkerset.sigs.k8s.io/worker-index": "2",
					"leaderworkerset.sigs.k8s.io/group-index":  "0",
				}, map[string]string{
					"leaderworkerset.sigs.k8s.io/leader-name": "serve-0",
				}),
				entryFor(lwsGVK), lwsInstances)

			Expect(result).To(Equal(Result{Component: "worker", Replica: "0"}))
		})

		It("degrades the component to the unknown sentinel when no selector matches", func() {
			result := Attribute(ctx, pod(nil, nil), entryFor(lwsGVK), lwsInstances)

			Expect(result.Component).To(Equal(collector.SentinelUnknown))
			Expect(result.Instance).To(Equal(collector.SentinelUnknown))
			Expect(result.Reason).To(Equal(collector.ReasonJQError))
		})
	})
})
