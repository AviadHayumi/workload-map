// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package flows

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	kartav1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/test/e2e/recorder"
)

// TrainJob states are judged from its own fields: only Suspended, Complete and Failed exist as
// conditions; a job with pods active and no terminal condition is running, and one with neither
// is initializing. Suspension requested while pods still drain reads Running, mirroring the
// karta: suspended is the condition, or spec.suspend with nothing active.
func trainjobRunning() recorder.StateCheck {
	return func(cr *unstructured.Unstructured) bool {
		jobs, _, _ := unstructured.NestedSlice(cr.Object, "status", "jobsStatus")
		for _, entry := range jobs {
			job, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			active, _, _ := unstructured.NestedInt64(job, "active")
			ready, _, _ := unstructured.NestedInt64(job, "ready")
			if active > 0 || ready > 0 {
				return true
			}
		}

		return false
	}
}

func trainjobSuspended() recorder.StateCheck {
	running := trainjobRunning()

	return func(cr *unstructured.Unstructured) bool {
		if CondTrue("Suspended")(cr) {
			return true
		}
		suspend, _, _ := unstructured.NestedBool(cr.Object, "spec", "suspend")

		return suspend && !running(cr)
	}
}

var _ = Describe("TrainJob (trainer)", Ordered, Label("trainer"), func() {
	var rec *recorder.Recorder
	var fx recorder.Fixture

	// The TrainJob karta reads the base pod template through its trainingRuntime reference, so
	// every flow captures the runtime alongside the workload: the replay resolves the reference
	// from the recording alone.
	runtime := recorder.CapturedReference{
		GVK:  kartav1alpha1.GroupVersionKind{Group: "trainer.kubeflow.org", Version: "v1alpha1", Kind: "ClusterTrainingRuntime"},
		Name: "karta-busybox",
	}

	BeforeAll(func(ctx SpecContext) {
		installKarta(ctx, "../../docs/catalog/trainer-kubeflow-org-trainjob-v1alpha1.yaml", "trainer-kubeflow-org-trainjob-v1alpha1")
		fx = recorder.Fixture{
			Operator:  "trainer",
			Version:   operatorVersion("trainer"),
			KartaName: "trainer-kubeflow-org-trainjob-v1alpha1",
			KartaFile: "docs/catalog/trainer-kubeflow-org-trainjob-v1alpha1.yaml",
		}
		rec = recorder.New(cfg).
			AddState(kartav1alpha1.InitializingStatus, AllOf(CondNotTrue("Complete"), CondNotTrue("Failed"), CondNotTrue("Suspended"))).
			AddState(kartav1alpha1.RunningStatus, trainjobRunning()).
			AddState(kartav1alpha1.SuspendedStatus, trainjobSuspended()).
			AddState(kartav1alpha1.CompletedStatus, CondTrue("Complete")).
			AddState(kartav1alpha1.FailedStatus, CondTrue("Failed"))
	})

	It("completed", func(ctx SpecContext) {
		out, err := recorder.NewFlow(rec, "completed", "testdata/trainer/completed.yaml").
			Capturing(runtime).
			Through(
				recorder.Reaches(kartav1alpha1.InitializingStatus).Optional(),
				recorder.Reaches(kartav1alpha1.RunningStatus).Optional(),
				recorder.Reaches(kartav1alpha1.CompletedStatus),
			).Run(ctx)
		Expect(rec.Save(fx, out)).Error().NotTo(HaveOccurred())
		Expect(err).To(Succeed())
	})

	It("suspended", func(ctx SpecContext) {
		out, err := recorder.NewFlow(rec, "suspended", "testdata/trainer/suspended.yaml").
			Capturing(runtime).
			Through(recorder.Reaches(kartav1alpha1.SuspendedStatus)).Run(ctx)
		Expect(rec.Save(fx, out)).Error().NotTo(HaveOccurred())
		Expect(err).To(Succeed())
	})

	// The full lifecycle: the job runs, is suspended mid-flight, resumes, and completes. Every
	// transition lands in the recording as its own frame, references captured with each.
	It("lifecycle", func(ctx SpecContext) {
		out, err := recorder.NewFlow(rec, "lifecycle", "testdata/trainer/lifecycle.yaml").
			Capturing(runtime).
			Through(
				recorder.Reaches(kartav1alpha1.InitializingStatus).Optional(),
				recorder.Reaches(kartav1alpha1.RunningStatus).Do(Suspend()),
				recorder.Reaches(kartav1alpha1.SuspendedStatus).Do(Resume()),
				// Right after resume the controller clears Suspended before jobsStatus catches
				// up, so a stale-active Running frame may precede the re-initialization dip.
				recorder.Reaches(kartav1alpha1.RunningStatus).Optional(),
				recorder.Reaches(kartav1alpha1.InitializingStatus).Optional(),
				recorder.Reaches(kartav1alpha1.RunningStatus).Optional(),
				recorder.Reaches(kartav1alpha1.CompletedStatus),
			).Run(ctx)
		Expect(rec.Save(fx, out)).Error().NotTo(HaveOccurred())
		Expect(err).To(Succeed())
	})

	It("failed", func(ctx SpecContext) {
		out, err := recorder.NewFlow(rec, "failed", "testdata/trainer/failed.yaml").
			Capturing(runtime).
			Through(
				recorder.Reaches(kartav1alpha1.InitializingStatus).Optional(),
				recorder.Reaches(kartav1alpha1.RunningStatus).Optional(),
				// The pod exits before the Failed condition lands, so jobsStatus zeroes into a
				// short re-initializing dip first.
				recorder.Reaches(kartav1alpha1.InitializingStatus).Optional(),
				recorder.Reaches(kartav1alpha1.FailedStatus),
			).Run(ctx)
		Expect(rec.Save(fx, out)).Error().NotTo(HaveOccurred())
		Expect(err).To(Succeed())
	})

	It("resumed", func(ctx SpecContext) {
		out, err := recorder.NewFlow(rec, "resumed", "testdata/trainer/suspended.yaml").
			Capturing(runtime).
			Through(
				recorder.Reaches(kartav1alpha1.SuspendedStatus).Do(Resume()),
				recorder.Reaches(kartav1alpha1.InitializingStatus).Optional(),
				recorder.Reaches(kartav1alpha1.RunningStatus).Optional(),
				recorder.Reaches(kartav1alpha1.CompletedStatus),
			).Run(ctx)
		Expect(rec.Save(fx, out)).Error().NotTo(HaveOccurred())
		Expect(err).To(Succeed())
	})
})
