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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type coordinate struct{ component, instance, group string }
type scenario struct {
	name, fixture, definition string
	phases                    []string
	groups                    map[coordinate]int
	ready                     func(*unstructured.Unstructured) bool
	removeTerminalPods        bool
}

var scenarios = []scenario{
	{name: "lws", fixture: "lws/running.yaml", definition: "leaderworkerset-x-k8s-io-leaderworkerset-v1", phases: []string{"Running"}, groups: map[coordinate]int{{"leader", "", "0"}: 1, {"worker", "", "0"}: 1}, ready: condition("Available")},
	{name: "jobset", fixture: "jobset/running.yaml", definition: "jobset-x-k8s-io-jobset-v1alpha2", phases: []string{"Running"}, groups: map[coordinate]int{{"replicatedjob", "workers", "0"}: 1}, ready: func(root *unstructured.Unstructured) bool {
		jobs, _, _ := unstructured.NestedSlice(root.Object, "status", "replicatedJobsStatus")
		for _, raw := range jobs {
			job, ok := raw.(map[string]any)
			if ok {
				ready, _, _ := unstructured.NestedInt64(job, "ready")
				if ready > 0 {
					return true
				}
			}
		}
		return false
	}},
	{name: "raycluster", fixture: "raycluster/running.yaml", definition: "ray-io-raycluster-v1", phases: []string{"Running"}, groups: map[coordinate]int{{"head", "", ""}: 1}, ready: fieldEquals("ready", "status", "state")},
	{name: "rayjob", fixture: "rayjob/running.yaml", definition: "ray-io-rayjob-v1", phases: []string{"Running"}, groups: map[coordinate]int{{"head", "", ""}: 1, {"submitter", "", ""}: 1}, ready: fieldEquals("RUNNING", "status", "jobStatus")},
	{name: "rayservice", fixture: "local:rayservice.yaml", definition: "ray-io-rayservice-v1", phases: []string{"Running"}, groups: map[coordinate]int{{"head", "", ""}: 1, {"worker", "workers", ""}: 1}, ready: condition("Ready")},
	{name: "pytorch", fixture: "pytorch/running.yaml", definition: "kubeflow-org-pytorchjob-v1", phases: []string{"Initializing", "Running"}, groups: map[coordinate]int{{"master", "", ""}: 1, {"worker", "", ""}: 1}, ready: condition("Running")},
	{name: "mpi", fixture: "mpijob/running.yaml", definition: "kubeflow-org-mpijob-v2beta1", phases: []string{"Initializing", "Running"}, groups: map[coordinate]int{{"launcher", "", ""}: 1, {"worker", "", ""}: 1}, ready: condition("Running")},
	{name: "knative", fixture: "knative/running.yaml", definition: "serving-knative-dev-service-v1", phases: []string{"Running"}, groups: map[coordinate]int{{"revision", "", ""}: 1}, ready: condition("Ready")},
	{name: "kserve", fixture: "kserve/running.yaml", definition: "serving-kserve-io-inferenceservice-v1beta1", phases: []string{"Running"}, groups: map[coordinate]int{{"predictor", "", ""}: 1}, ready: condition("Ready")},
	{name: "llmisvc", fixture: "llmisvc/running.yaml", definition: "serving-kserve-io-llminferenceservice-v1alpha2", phases: []string{"Running"}, groups: map[coordinate]int{{"main", "", ""}: 1}, ready: condition("Ready")},
	{name: "milvus", fixture: "milvus/running.yaml", definition: "milvus-io-milvus-v1beta1", phases: []string{"Running"}, groups: map[coordinate]int{{"standalone", "", ""}: 1}, ready: condition("MilvusReady")},
	{name: "grove", fixture: "grove/running.yaml", definition: "grove-io-podcliqueset-v1alpha1", phases: []string{"Running"}, groups: map[coordinate]int{{"clique", "worker", "0"}: 2}, ready: func(root *unstructured.Unstructured) bool {
		available, _, _ := unstructured.NestedInt64(root.Object, "status", "availableReplicas")
		return available == 1
	}},
	{name: "dynamo", fixture: "dynamo/running.yaml", definition: "nvidia-com-dynamographdeployment-v1alpha1", phases: []string{"Running"}, groups: map[coordinate]int{{"service", "Frontend", ""}: 1, {"service", "decode", ""}: 1}, ready: fieldEquals("successful", "status", "state")},
	{name: "nim", fixture: "nim/running.yaml", definition: "apps-nvidia-com-nimservice-v1alpha1", phases: []string{"Running"}, groups: map[coordinate]int{{"nimservice", "", ""}: 1}, ready: fieldEquals("Ready", "status", "state")},
	{name: "sandbox", fixture: "sandbox/running.yaml", definition: "agents-x-k8s-io-sandbox-v1beta1", phases: []string{"Running"}, groups: map[coordinate]int{{"agent", "", ""}: 1}, ready: condition("Ready")},
	{name: "job-zero-suspended", fixture: "batch-job/suspended.yaml", definition: "batch-job-v1", phases: []string{"Suspended"}, groups: map[coordinate]int{}, ready: condition("Suspended")},
	{name: "job-zero-completed", fixture: "batch-job/completed.yaml", definition: "batch-job-v1", phases: []string{"Completed"}, groups: map[coordinate]int{}, ready: condition("Complete"), removeTerminalPods: true},
	{name: "job-zero-failed", fixture: "batch-job/failed.yaml", definition: "batch-job-v1", phases: []string{"Failed"}, groups: map[coordinate]int{}, ready: condition("Failed"), removeTerminalPods: true},
}

var _ = Describe("operator workloads", Serial, func() {
	for _, test := range scenarios {
		It(test.name, Label(test.name), func(ctx SpecContext) {
			namespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"generateName": "exporter-" + test.name + "-", "labels": map[string]any{"exporter-e2e": "true"}}}}
			namespace, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Create(ctx, namespace, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func(ctx SpecContext) {
				_ = client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Delete(ctx, namespace.GetName(), metav1.DeleteOptions{})
			})
			prepare(ctx, test.name, namespace.GetName())
			root, resource, err := createFixture(ctx, test.fixture, namespace.GetName())
			Expect(err).NotTo(HaveOccurred())
			var current *unstructured.Unstructured
			Eventually(ctx, func() error {
				var err error
				current, err = client.Resource(resource).Namespace(namespace.GetName()).Get(ctx, root.GetName(), metav1.GetOptions{})
				if err != nil {
					return err
				}
				if !test.ready(current) {
					return fmt.Errorf("%s controller has not reached expected raw status: %v", test.name, current.Object["status"])
				}
				return nil
			}, 10*time.Minute, 2*time.Second).Should(Succeed())
			if test.removeTerminalPods {
				pods, err := ownedPods(ctx, current)
				Expect(err).NotTo(HaveOccurred())
				Expect(pods).NotTo(BeEmpty(), "controller must complete a real Pod before the zero-Pod assertion")
				for _, pod := range pods {
					Expect(client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace.GetName()).Delete(ctx, pod.GetName(), metav1.DeleteOptions{})).To(Succeed())
				}
			}
			Eventually(ctx, func() error { return verifyScenario(ctx, test, current, "initial", "initial") }, 2*time.Minute, time.Second).Should(Succeed())
			patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"labels": map[string]string{teamLabel: "updated"}}})
			current, err = client.Resource(resource).Namespace(namespace.GetName()).Patch(ctx, root.GetName(), types.MergePatchType, patch, metav1.PatchOptions{})
			Expect(err).NotTo(HaveOccurred())
			Eventually(ctx, func() error { return verifyScenario(ctx, test, current, "updated", "updated") }, 2*time.Minute, time.Second).Should(Succeed())
			beforeDelete, err := ownedPods(ctx, current)
			Expect(err).NotTo(HaveOccurred())
			removedPods := make(map[string]bool)
			for _, pod := range beforeDelete {
				removedPods[string(pod.GetUID())] = true
			}
			policy := metav1.DeletePropagationForeground
			Expect(client.Resource(resource).Namespace(namespace.GetName()).Delete(ctx, root.GetName(), metav1.DeleteOptions{PropagationPolicy: &policy})).To(Succeed())
			Eventually(ctx, func() error {
				_, err := client.Resource(resource).Namespace(namespace.GetName()).Get(ctx, root.GetName(), metav1.GetOptions{})
				if err == nil {
					return fmt.Errorf("root deletion has not completed")
				}
				if !apierrors.IsNotFound(err) {
					return err
				}
				rows, raw, err := scrape(ctx)
				if err != nil {
					return err
				}
				for _, row := range rows {
					if row.labels["workload_uid"] == string(root.GetUID()) || removedPods[row.labels["pod_uid"]] {
						return fmt.Errorf("deleted root %s remains in %s", root.GetUID(), row.family)
					}
				}
				return os.WriteFile(filepath.Join(outputDir, test.name+"-deleted.prom"), raw, 0600)
			}, 2*time.Minute, time.Second).Should(Succeed())
			AddReportEntry("verified", fmt.Sprintf("%s namespace=%s UID=%s phases=%v", test.name, namespace.GetName(), root.GetUID(), test.phases))
		}, SpecTimeout(15*time.Minute))
	}
})

func condition(name string) func(*unstructured.Unstructured) bool {
	return func(root *unstructured.Unstructured) bool {
		conditions, _, _ := unstructured.NestedSlice(root.Object, "status", "conditions")
		for _, raw := range conditions {
			value, ok := raw.(map[string]any)
			if ok && value["type"] == name && value["status"] == "True" {
				return true
			}
		}
		return false
	}
}
func fieldEquals(want string, path ...string) func(*unstructured.Unstructured) bool {
	return func(root *unstructured.Unstructured) bool {
		actual, _, _ := unstructured.NestedString(root.Object, path...)
		return actual == want
	}
}

func prepare(ctx context.Context, name, namespace string) {
	switch name {
	case "rayservice":
		_, _, err := createFixture(ctx, "local:ray-app.yaml", namespace)
		Expect(err).NotTo(HaveOccurred())
	case "llmisvc":
		object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "karta-e2e-llmisvc-model"}, "spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "16Mi"}}}}}
		_, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(namespace).Create(ctx, object, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
	case "dynamo", "nim":
		secret, key := "hf-token-secret", "HF_TOKEN"
		if name == "nim" {
			secret, key = "ngc-secret", "NGC_API_KEY"
		}
		object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": secret}, "stringData": map[string]any{key: "placeholder"}}}
		_, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace(namespace).Create(ctx, object, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
	}
}
