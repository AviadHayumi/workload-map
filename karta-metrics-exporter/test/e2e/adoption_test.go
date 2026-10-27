//go:build e2e

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

type adoptionWatchEvidence struct {
	ResourceVersion string        `json:"resourceVersion"`
	StartedAt       time.Time     `json:"startedAt"`
	FinishedAt      time.Time     `json:"finishedAt"`
	Events          []watch.Event `json:"events"`
	Error           string        `json:"error,omitempty"`
}

var _ = Describe("native owner adoption", Serial, func() {
	if apiProfile != "default" {
		return
	}

	It("reattributes an unchanged Pod when a replacement Deployment adopts its ReplicaSet", Label("deployment-adoption"), func(ctx SpecContext) {
		var test scenario
		for _, candidate := range defaultScenarios {
			if candidate.name == "deployment" {
				test = candidate
			}
		}
		Expect(test.fixture).NotTo(BeEmpty())
		test.name = "deployment-adoption"
		namespaces := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
		namespace, err := namespaces.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{
				"generateName": "exporter-adoption-", "labels": map[string]any{"exporter-e2e": "true"},
			},
		}}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) {
			Expect(namespaces.Delete(ctx, namespace.GetName(), metav1.DeleteOptions{})).To(Succeed())
		})
		evidence := map[string]any{"namespace": namespace.GetName()}
		DeferCleanup(func() {
			raw, err := json.MarshalIndent(evidence, "", "  ")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(outputDir, "deployment-adoption-api.json"), raw, 0600)).To(Succeed())
		})

		root, resource, err := createFixture(ctx, test.fixture, namespace.GetName())
		Expect(err).NotTo(HaveOccurred())
		rootName := root.GetName()
		deployments := client.Resource(resource).Namespace(namespace.GetName())
		pods := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace.GetName())
		replicaSets := client.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}).Namespace(namespace.GetName())
		Eventually(ctx, func() error {
			root, err = deployments.Get(ctx, rootName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if !test.ready(root) {
				return fmt.Errorf("Deployment is not Available: %v", root.Object["status"])
			}
			return verifyScenario(ctx, test, root, "initial", "before")
		}, 3*time.Minute, time.Second).Should(Succeed())

		podList, err := pods.List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(podList.Items).To(HaveLen(1))
		beforePod := podList.Items[0].DeepCopy()
		Expect(condition("Ready")(beforePod)).To(BeTrue())
		Expect(fieldEquals("Running", "status", "phase")(beforePod)).To(BeTrue())
		podOwner := metav1.GetControllerOf(beforePod)
		Expect(podOwner).NotTo(BeNil())
		Expect(podOwner.Kind).To(Equal("ReplicaSet"))
		beforeRS, err := replicaSets.Get(ctx, podOwner.Name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(beforeRS.GetUID()).To(Equal(podOwner.UID))
		beforeOwner := metav1.GetControllerOf(beforeRS)
		Expect(beforeOwner).NotTo(BeNil())
		Expect(beforeOwner.UID).To(Equal(root.GetUID()))
		evidence["before"] = map[string]any{"deployment": root, "replicaSet": beforeRS, "pod": beforePod}

		// Start at the LIST resource version so updates between LIST and WATCH
		// cannot silently turn this into a Pod-event-driven adoption test.
		watchCtx, cancelWatch := context.WithCancel(ctx)
		defer cancelWatch()
		watchStarted := time.Now()
		podWatch, err := pods.Watch(watchCtx, metav1.ListOptions{
			ResourceVersion: podList.GetResourceVersion(),
			FieldSelector:   fields.OneTermEqualSelector("metadata.name", beforePod.GetName()).String(),
		})
		Expect(err).NotTo(HaveOccurred())
		watchResult := make(chan adoptionWatchEvidence, 1)
		go func() {
			result := adoptionWatchEvidence{ResourceVersion: podList.GetResourceVersion(), StartedAt: watchStarted, Events: []watch.Event{}}
			defer func() {
				podWatch.Stop()
				result.FinishedAt = time.Now()
				watchResult <- result
			}()
			for {
				select {
				case <-watchCtx.Done():
					return
				case event, open := <-podWatch.ResultChan():
					if !open {
						if watchCtx.Err() == nil {
							result.Error = "Pod watch closed before adoption verification completed"
						}
						return
					}
					if event.Type != watch.Bookmark {
						result.Events = append(result.Events, event)
					}
				}
			}
		}()
		DeferCleanup(func() {
			cancelWatch()
			select {
			case result := <-watchResult:
				evidence["podWatch"] = result
				Expect(result.Error).To(BeEmpty())
				Expect(result.Events).To(BeEmpty(), "adoption must converge without any Pod watch event")
			case <-time.After(5 * time.Second):
				Fail("Pod watch did not stop within its cleanup deadline")
			}
		})

		policy := metav1.DeletePropagationOrphan
		Expect(deployments.Delete(ctx, root.GetName(), metav1.DeleteOptions{PropagationPolicy: &policy})).To(Succeed())
		var orphanRS *unstructured.Unstructured
		Eventually(ctx, func() error {
			_, err := deployments.Get(ctx, root.GetName(), metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("original Deployment deletion is incomplete: %v", err)
			}
			orphanRS, err = replicaSets.Get(ctx, beforeRS.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}
			if orphanRS.GetUID() != beforeRS.GetUID() || metav1.GetControllerOf(orphanRS) != nil {
				return fmt.Errorf("original ReplicaSet is not orphaned")
			}
			return nil
		}, time.Minute, time.Second).Should(Succeed())
		orphanPod, err := pods.Get(ctx, beforePod.GetName(), metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(orphanPod.GetUID()).To(Equal(beforePod.GetUID()))
		Expect(orphanPod.GetResourceVersion()).To(Equal(beforePod.GetResourceVersion()))
		Expect(orphanPod.GetOwnerReferences()).To(Equal(beforePod.GetOwnerReferences()))
		evidence["orphan"] = map[string]any{"replicaSet": orphanRS, "pod": orphanPod}

		replacement, _, err := createFixture(ctx, test.fixture, namespace.GetName())
		Expect(err).NotTo(HaveOccurred())
		Expect(replacement.GetName()).To(Equal(root.GetName()))
		Expect(replacement.GetUID()).NotTo(Equal(root.GetUID()))
		Expect(replacement.Object["spec"]).To(Equal(root.Object["spec"]))
		Eventually(ctx, func() error {
			replacement, err = deployments.Get(ctx, rootName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			afterRS, err := replicaSets.Get(ctx, beforeRS.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}
			afterPod, err := pods.Get(ctx, beforePod.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}
			evidence["after"] = map[string]any{"deployment": replacement, "replicaSet": afterRS, "pod": afterPod}
			if afterPod.GetUID() != beforePod.GetUID() || afterPod.GetResourceVersion() != beforePod.GetResourceVersion() || !reflect.DeepEqual(afterPod.GetOwnerReferences(), beforePod.GetOwnerReferences()) {
				return StopTrying("Pod changed during native adoption")
			}
			owner := metav1.GetControllerOf(afterRS)
			if afterRS.GetUID() != beforeRS.GetUID() || owner == nil || owner.UID != replacement.GetUID() {
				return fmt.Errorf("replacement Deployment has not adopted the original ReplicaSet")
			}
			actualRoot, err := ownerRoot(ctx, afterPod, map[types.UID]*unstructured.Unstructured{})
			if err != nil {
				return err
			}
			if actualRoot.GetUID() != replacement.GetUID() || !test.ready(replacement) {
				return fmt.Errorf("replacement ownership or controller status is not ready: %v", replacement.Object["status"])
			}
			if err := verifyScenario(ctx, test, replacement, "initial", "after"); err != nil {
				return err
			}
			rows, raw, err := scrape(ctx)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(outputDir, "deployment-adoption-old-root-absent.prom"), raw, 0600); err != nil {
				return err
			}
			for _, row := range rows {
				if row.labels["workload_uid"] == string(root.GetUID()) {
					return fmt.Errorf("old Deployment UID remains in %s", row.family)
				}
			}
			return nil
		}, 2*time.Minute, time.Second).Should(Succeed())
		finalPod, err := pods.Get(ctx, beforePod.GetName(), metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		evidence["podAfterMetricVerification"] = finalPod
		Expect(finalPod.GetUID()).To(Equal(beforePod.GetUID()))
		Expect(finalPod.GetResourceVersion()).To(Equal(beforePod.GetResourceVersion()))
		Expect(finalPod.GetOwnerReferences()).To(Equal(beforePod.GetOwnerReferences()))
		evidence["podIdentityResourceVersionAndOwnersUnchanged"] = true
		AddReportEntry("verified", fmt.Sprintf("native adoption namespace=%s old=%s new=%s ReplicaSet=%s Pod=%s unchanged RV=%s", namespace.GetName(), root.GetUID(), replacement.GetUID(), beforeRS.GetUID(), beforePod.GetUID(), beforePod.GetResourceVersion()))
	}, SpecTimeout(8*time.Minute))
})
