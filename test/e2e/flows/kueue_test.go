// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package flows

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kartav1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/test/e2e/recorder"
)

var _ = Describe("Kueue Workload", Ordered, Label("kueue", "workload"), func() {
	It("running", func(ctx SpecContext) {
		installKarta(ctx, "../../docs/catalog/kueue-x-k8s-io-workload-v1beta2.yaml", "kueue-x-k8s-io-workload-v1beta2")
		queue := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "LocalQueue",
			"metadata": map[string]any{"name": "karta-e2e", "namespace": testNamespace},
			"spec":     map[string]any{"clusterQueue": "karta-e2e", "stopPolicy": "Hold"},
		}}
		Expect(k8sClient.Create(ctx, queue)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { _ = k8sClient.Delete(ctx, queue) })

		for _, file := range []string{"running-head.yaml", "running-worker.yaml"} {
			pod := &unstructured.Unstructured{}
			Expect(yaml.Unmarshal(readE2E("flows/testdata/workload/"+file), pod)).To(Succeed())
			pod.SetNamespace(testNamespace)
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) { _ = k8sClient.Delete(ctx, pod) })
		}

		workload := &unstructured.Unstructured{}
		workload.SetAPIVersion("kueue.x-k8s.io/v1beta2")
		workload.SetKind("Workload")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testNamespace, Name: "karta-e2e-workload-running",
			}, workload)).To(Succeed())
			g.Expect(CondFalse("Admitted")(workload)).To(BeTrue())
		}, time.Minute, time.Second).Should(Succeed())

		// Observe starts from this queued object's resourceVersion, so the
		// watch includes every update after releasing the namespace's queue.
		Expect(k8sClient.Patch(ctx, queue, client.RawPatch(types.MergePatchType,
			[]byte(`{"spec":{"stopPolicy":"None"}}`)))).To(Succeed())
		rec := recorder.New(cfg).SetTimeout(4*time.Minute).
			AddState(kartav1alpha1.InitializingStatus, AllOf(CondFalse("Admitted"), CondNotTrue("Finished"))).
			AddState(kartav1alpha1.RunningStatus, AllOf(CondTrue("Admitted"), CondTrue("PodsReady"),
				CondNotTrue("Finished"), CondNotTrue("Evicted"), CondNotTrue("WaitingForReplacementPods")))
		out, err := recorder.NewFlow(rec, "running", "").Through(
			recorder.Reaches(kartav1alpha1.InitializingStatus),
			recorder.Reaches(kartav1alpha1.RunningStatus),
		).Observe(ctx, workload)
		Expect(rec.Save(recorder.Fixture{
			Operator: "kueue", Version: operatorVersion("kueue"),
			KartaName: "kueue-x-k8s-io-workload-v1beta2",
			KartaFile: "docs/catalog/kueue-x-k8s-io-workload-v1beta2.yaml",
		}, out)).Error().NotTo(HaveOccurred())
		Expect(err).To(Succeed())
	})
})
