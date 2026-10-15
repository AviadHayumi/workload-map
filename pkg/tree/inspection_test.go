// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package tree

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

var _ = Describe("root inspection", func() {
	var object *unstructured.Unstructured

	BeforeEach(func() {
		object = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata":   map[string]any{"name": "example"},
			"spec": map[string]any{
				"parallelism": int64(5),
				"replicas":    int64(3),
				"template": map[string]any{
					"metadata": map[string]any{"labels": map[string]any{"app": "example"}},
					"spec": map[string]any{"containers": []any{
						map[string]any{"name": "work", "image": "ghcr.io/example/work:v1"},
					}},
				},
			},
		}}
	})

	DescribeTable("retains pod-bearing root data",
		func(definition func() *v1alpha1.Karta, replicas int32, childNames []string) {
			karta := definition()
			kind := karta.Spec.StructureDefinition.RootComponent.Kind
			object.SetAPIVersion(kind.Group + "/" + kind.Version)
			object.SetKind(kind.Kind)
			result, err := Build(context.Background(), resource.NewComponentFactoryFromObject(karta, object))
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Root).NotTo(BeNil())
			Expect(result.Root.Name).To(Equal(karta.Spec.StructureDefinition.RootComponent.Name))
			Expect(result.Root.HasPodDefinition).To(BeTrue())
			Expect(result.Root.Instances).To(HaveLen(1))
			instance := result.Root.Instances[0]
			Expect(instance.Scale).To(Equal(&resource.Scale{Replicas: ptr.To(replicas)}))
			Expect(instance.ExtractedInstance).NotTo(BeNil())
			Expect(instance.ExtractedInstance.PodTemplateSpec).To(Equal(&corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "example"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "work", Image: "ghcr.io/example/work:v1"}}},
			}))
			Expect(componentNames(result.Children)).To(ConsistOf(childNames))
			Expect(result.Children).To(Equal(instance.Children))
			var visited []string
			for component := range result.Instances() {
				visited = append(visited, component.Name)
			}
			Expect(visited).To(ConsistOf(childNames))
		},
		Entry("Job keeps the legacy empty child view", kartas.BatchJob, int32(5), []string{}),
		Entry("Deployment keeps ReplicaSet as its only child", kartas.Deployment, int32(3), []string{"replicaset"}),
	)

	It("keeps nested descendants once in the root and legacy child views", func() {
		definition := bareJobKarta()
		definition.Spec.StructureDefinition.ChildComponents = []v1alpha1.ComponentDefinition{
			{Name: "group", OwnerRef: ptr.To("job")},
			{Name: "worker", OwnerRef: ptr.To("group"), SpecDefinition: &v1alpha1.SpecDefinition{
				PodTemplateSpecPath: ptr.To(".spec.template"),
			}},
		}
		result, err := Build(context.Background(), resource.NewComponentFactoryFromObject(definition, object))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Root.Instances).To(HaveLen(1))
		groups := result.Root.Instances[0].Children
		Expect(groups).To(HaveLen(1))
		Expect(groups[0].Name).To(Equal("group"))
		Expect(groups[0].Instances).To(HaveLen(1))
		workers := groups[0].Instances[0].Children
		Expect(workers).To(HaveLen(1))
		Expect(workers[0].Name).To(Equal("worker"))
		Expect(result.Children).To(Equal(groups))
		var visited []string
		for component := range result.Instances() {
			visited = append(visited, component.Name)
		}
		Expect(visited).To(Equal([]string{"group", "worker"}))
	})

	It("retains raw phase and complete conditions alongside normalized phases", func() {
		definition := bareJobKarta()
		definition.Spec.StructureDefinition.RootComponent.StatusDefinition = &v1alpha1.StatusDefinition{
			PhaseDefinition: &v1alpha1.PhaseDefinition{Path: ".status.phase"},
			ConditionsDefinition: &v1alpha1.ConditionsDefinition{
				Path: ".status.conditions", TypeFieldName: "type", StatusFieldName: "status",
				ReasonFieldName: ptr.To("reason"), MessageFieldName: ptr.To("message"),
			},
			StatusMappings: v1alpha1.StatusMappings{
				Running: []v1alpha1.StatusMatcher{{ByConditions: []v1alpha1.ExpectedCondition{
					{Type: "Ready", Status: ptr.To("True")},
				}}},
			},
		}
		object.Object["status"] = map[string]any{
			"phase": "Active",
			"conditions": []any{map[string]any{
				"type": "Ready", "status": "True", "reason": "WorkersReady", "message": "All workers are ready",
			}},
		}
		result, err := Build(context.Background(), resource.NewComponentFactoryFromObject(definition, object))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RootStatus).To(Equal(&resource.Status{
			Phase: ptr.To("Active"),
			Conditions: []resource.Condition{{
				Type: "Ready", Status: ptr.To("True"), Reason: ptr.To("WorkersReady"), Message: "All workers are ready",
			}},
			MatchedStatuses: []v1alpha1.ResourceStatus{v1alpha1.RunningStatus},
		}))
		Expect(result.Status).To(Equal(&WorkloadStatus{Phases: []string{"Running"}}))
	})

	It("still rejects a root without a status definition", func() {
		definition := bareJobKarta()
		definition.Spec.StructureDefinition.RootComponent.StatusDefinition = nil
		result, err := Build(context.Background(), resource.NewComponentFactoryFromObject(definition, object))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("root component must have status definition"))
		Expect(result).To(BeNil())
	})

	DescribeTable("propagates root extraction failures",
		func(update func(*v1alpha1.ComponentDefinition)) {
			definition := bareJobKarta()
			update(&definition.Spec.StructureDefinition.RootComponent)
			result, err := Build(context.Background(), resource.NewComponentFactoryFromObject(definition, object))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("root extraction failed"))
			Expect(result).To(BeNil())
		},
		Entry("template", func(root *v1alpha1.ComponentDefinition) {
			root.SpecDefinition.PodTemplateSpecPath = ptr.To(`error("root extraction failed")`)
		}),
		Entry("scale", func(root *v1alpha1.ComponentDefinition) {
			root.ScaleDefinition.ReplicasPath = ptr.To(`error("root extraction failed")`)
		}),
		Entry("status", func(root *v1alpha1.ComponentDefinition) {
			root.StatusDefinition.PhaseDefinition = &v1alpha1.PhaseDefinition{Path: `error("root extraction failed")`}
		}),
	)
})

var _ = Describe("Instances", func() {
	var result *WorkloadTree

	BeforeEach(func() {
		children := []ComponentNode{{Name: "group", Instances: []InstanceNode{
			{InstanceKey: ptr.To("first"), Children: []ComponentNode{
				{Name: "worker", Instances: []InstanceNode{{InstanceKey: ptr.To("a")}, {InstanceKey: ptr.To("b")}}},
			}},
			{InstanceKey: ptr.To("second"), Children: []ComponentNode{
				{Name: "worker", Instances: []InstanceNode{{InstanceKey: ptr.To("c")}}},
			}},
		}}}
		result = &WorkloadTree{
			Root:     &ComponentNode{Name: "root", Instances: []InstanceNode{{Children: children}}},
			Children: children,
		}
	})

	It("walks nested child instances once in depth-first order", func() {
		var visited []string
		for component, instance := range result.Instances() {
			visited = append(visited, component.Name+"/"+ptr.Deref(instance.InstanceKey, ""))
		}
		Expect(visited).To(Equal([]string{"group/first", "worker/a", "worker/b", "group/second", "worker/c"}))
	})

	It("stops after the consumer breaks inside a nested component", func() {
		var visited []string
		for component, instance := range result.Instances() {
			visited = append(visited, component.Name+"/"+ptr.Deref(instance.InstanceKey, ""))
			if component.Name == "worker" {
				break
			}
		}
		Expect(visited).To(Equal([]string{"group/first", "worker/a"}))
	})

	It("does not visit an empty tree", func() {
		result = &WorkloadTree{}
		for range result.Instances() {
			Fail("empty tree yielded an instance")
		}
	})
})
