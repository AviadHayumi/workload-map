// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ownership

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

var _ = Describe("Index", func() {
	var index *Index

	jobsetGroupKind := schema.GroupKind{Group: "jobset.x-k8s.io", Kind: "JobSet"}
	isJobsetRoot := func(groupKind schema.GroupKind) bool { return groupKind == jobsetGroupKind }

	ref := func(apiVersion, kind, name, uid string) metav1.OwnerReference {
		return metav1.OwnerReference{
			APIVersion: apiVersion,
			Kind:       kind,
			Name:       name,
			UID:        types.UID(uid),
			Controller: ptr.To(true),
		}
	}

	BeforeEach(func() {
		index = New()
	})

	It("finds a root referenced directly by the pod", func() {
		index.UpsertObject("root-1", nil)

		result := index.RootFor([]metav1.OwnerReference{ref("jobset.x-k8s.io/v1alpha2", "JobSet", "llm", "root-1")}, isJobsetRoot)

		Expect(result.Outcome).To(Equal(OutcomeFound))
		Expect(result.Root.UID).To(Equal(types.UID("root-1")))
		Expect(result.Root.Name).To(Equal("llm"))
	})

	It("walks through a middle object to the root", func() {
		index.UpsertObject("root-1", nil)
		index.UpsertObject("job-1", []metav1.OwnerReference{ref("jobset.x-k8s.io/v1alpha2", "JobSet", "llm", "root-1")})

		result := index.RootFor([]metav1.OwnerReference{ref("batch/v1", "Job", "llm-prefill-0", "job-1")}, isJobsetRoot)

		Expect(result.Outcome).To(Equal(OutcomeFound))
		Expect(result.Root.UID).To(Equal(types.UID("root-1")))
	})

	It("matches roots by group and kind regardless of version", func() {
		index.UpsertObject("root-1", nil)

		result := index.RootFor([]metav1.OwnerReference{ref("jobset.x-k8s.io/v9", "JobSet", "llm", "root-1")}, isJobsetRoot)

		Expect(result.Outcome).To(Equal(OutcomeFound))
	})

	It("reports a missing middle owner and drains it later", func() {
		result := index.RootFor([]metav1.OwnerReference{ref("batch/v1", "Job", "llm-prefill-0", "job-1")}, isJobsetRoot)
		Expect(result.Outcome).To(Equal(OutcomeMissing))
		Expect(result.Missing).To(Equal(types.UID("job-1")))

		index.MarkPending(result.Missing, "team-a/prefill-0")
		Expect(index.PendingCount()).To(Equal(1))

		drained := index.UpsertObject("job-1", []metav1.OwnerReference{ref("jobset.x-k8s.io/v1alpha2", "JobSet", "llm", "root-1")})
		Expect(drained).To(ConsistOf("team-a/prefill-0"))
		Expect(index.PendingCount()).To(BeZero())
	})

	It("reports objects without a controller owner", func() {
		orphan := ref("batch/v1", "Job", "job", "job-1")
		orphan.Controller = ptr.To(false)

		Expect(index.RootFor(nil, isJobsetRoot).Outcome).To(Equal(OutcomeNoController))
		Expect(index.RootFor([]metav1.OwnerReference{orphan}, isJobsetRoot).Outcome).To(Equal(OutcomeNoController))
	})

	It("reports a missing owner again after the middle object was recreated", func() {
		index.UpsertObject("job-1", []metav1.OwnerReference{ref("jobset.x-k8s.io/v1alpha2", "JobSet", "llm", "root-1")})
		index.DeleteObject("job-1")

		result := index.RootFor([]metav1.OwnerReference{ref("batch/v1", "Job", "llm-prefill-0", "job-1")}, isJobsetRoot)
		Expect(result.Outcome).To(Equal(OutcomeMissing))
	})

	It("caps the walk depth on ownership cycles", func() {
		index.UpsertObject("a", []metav1.OwnerReference{ref("example.io/v1", "Middle", "b", "b")})
		index.UpsertObject("b", []metav1.OwnerReference{ref("example.io/v1", "Middle", "a", "a")})

		result := index.RootFor([]metav1.OwnerReference{ref("example.io/v1", "Middle", "a", "a")}, isJobsetRoot)
		Expect(result.Outcome).To(Equal(OutcomeDepthExceeded))
	})

	It("walks through a pod acting as a middle owner (the LeaderWorkerSet chain)", func() {
		lwsGroupKind := schema.GroupKind{Group: "leaderworkerset.x-k8s.io", Kind: "LeaderWorkerSet"}
		isLWSRoot := func(groupKind schema.GroupKind) bool { return groupKind == lwsGroupKind }

		index.UpsertObject("lws-1", nil)
		index.UpsertObject("leader-sts", []metav1.OwnerReference{ref("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet", "serve", "lws-1")})
		index.UpsertObject("leader-pod", []metav1.OwnerReference{ref("apps/v1", "StatefulSet", "serve", "leader-sts")})
		index.UpsertObject("worker-sts", []metav1.OwnerReference{ref("", "Pod", "serve-0", "leader-pod")})

		result := index.RootFor([]metav1.OwnerReference{ref("apps/v1", "StatefulSet", "serve-0", "worker-sts")}, isLWSRoot)

		Expect(result.Outcome).To(Equal(OutcomeFound))
		Expect(result.Root.UID).To(Equal(types.UID("lws-1")))
	})

	It("attributes to the outermost registered root when kinds nest", func() {
		nestedRoots := func(groupKind schema.GroupKind) bool {
			return groupKind == jobsetGroupKind || groupKind == schema.GroupKind{Group: "batch", Kind: "Job"}
		}
		index.UpsertObject("root-1", nil)
		index.UpsertObject("job-1", []metav1.OwnerReference{ref("jobset.x-k8s.io/v1alpha2", "JobSet", "llm", "root-1")})

		result := index.RootFor([]metav1.OwnerReference{ref("batch/v1", "Job", "llm-prefill-0", "job-1")}, nestedRoots)

		Expect(result.Outcome).To(Equal(OutcomeFound))
		Expect(result.Root.GVK.Kind).To(Equal("JobSet"))
	})

	It("returns the inner root when the chain ends there", func() {
		batchRoot := func(groupKind schema.GroupKind) bool {
			return groupKind == schema.GroupKind{Group: "batch", Kind: "Job"}
		}
		index.UpsertObject("job-1", nil)

		result := index.RootFor([]metav1.OwnerReference{ref("batch/v1", "Job", "standalone", "job-1")}, batchRoot)

		Expect(result.Outcome).To(Equal(OutcomeFound))
		Expect(result.Root.GVK.Kind).To(Equal("Job"))
	})

	It("parks instead of settling for an inner root when the chain is incomplete", func() {
		nestedRoots := func(groupKind schema.GroupKind) bool {
			return groupKind == jobsetGroupKind || groupKind == schema.GroupKind{Group: "batch", Kind: "Job"}
		}

		result := index.RootFor([]metav1.OwnerReference{ref("batch/v1", "Job", "llm-prefill-0", "job-1")}, nestedRoots)

		Expect(result.Outcome).To(Equal(OutcomeMissing))
		Expect(result.Missing).To(Equal(types.UID("job-1")))
	})

	It("forgets pending pods on pod deletion", func() {
		index.MarkPending("job-1", "team-a/prefill-0")
		index.ForgetPending("team-a/prefill-0")

		Expect(index.PendingCount()).To(BeZero())
	})
})
