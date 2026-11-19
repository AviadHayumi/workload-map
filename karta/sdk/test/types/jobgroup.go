// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// +kubebuilder:object:generate=true

package types

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/utils/ptr"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// JobGroup represents a JobSet-like job with array of replicated jobs
// Multiple components via array iteration, separate spec + metadata
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type JobGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              JobGroupSpec   `json:"spec,omitempty"`
	Status            JobGroupStatus `json:"status,omitempty"`
}

type JobGroupSpec struct {
	// ReplicatedJobs defines an array of job specifications
	ReplicatedJobs []ReplicatedJob `json:"replicatedJobs,omitempty"`
}

type ReplicatedJob struct {
	// Name identifies this job within the array
	Name string `json:"name"`

	// Replicas is the desired number of replicas for this job
	Replicas int32 `json:"replicas"`

	// Spec defines the pod specification (direct, no template wrapper)
	Spec corev1.PodSpec `json:"spec"`

	// Metadata defines the pod metadata (direct, no template wrapper)
	Metadata metav1.ObjectMeta `json:"metadata"`
}

type JobGroupStatus struct {
	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// JobGroupKarta returns a Karta for JobGroup
// Models JobSet-like structure: array components, separate pod spec + metadata extraction
func JobGroupKarta() *v1alpha1.Karta {
	return &v1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{
			Name: "jobgroup",
		},
		Spec: v1alpha1.KartaSpec{
			StructureDefinition: v1alpha1.StructureDefinition{
				RootComponent: v1alpha1.ComponentDefinition{
					Name: "jobgroup",
					Kind: &v1alpha1.GroupVersionKind{
						Group:   "jobs.example.com",
						Version: "v1",
						Kind:    "JobGroup",
					},
					StatusDefinition: &v1alpha1.StatusDefinition{
						ConditionsDefinition: &v1alpha1.ConditionsDefinition{
							Read:            v1alpha1.ReadAccessor{Expression: `object[?"status"][?"conditions"].orValue(dyn(null))`},
							TypeFieldName:   "type",
							StatusFieldName: "status",
						},
						StatusMappings: v1alpha1.StatusMappings{
							Running: []v1alpha1.StatusMatcher{
								{
									ByConditions: []v1alpha1.ExpectedCondition{
										{
											Type:   "Running",
											Status: ptr.To("True"),
										},
									},
								},
							},
						},
					},
				},
				ChildComponents: []v1alpha1.ComponentDefinition{
					{
						Name:        "job",
						OwnerRef:    ptr.To("jobgroup"),
						InstanceIds: &v1alpha1.ValueAccessor{Read: &v1alpha1.ReadAccessor{Expression: `(object[?"spec"][?"replicatedJobs"].orValue([])).map(x, x[?"name"].orValue(dyn(null)))`}},
						SpecDefinition: &v1alpha1.SpecDefinition{
							PodSpec:  &v1alpha1.ValueAccessor{Read: &v1alpha1.ReadAccessor{Expression: `(object[?"spec"][?"replicatedJobs"].orValue([])).map(x, x[?"spec"].orValue(dyn(null)))`}, Write: &v1alpha1.WriteAccessor{PathExpression: `"/spec/replicatedJobs/" + string(index) + "/spec"`}},
							Metadata: &v1alpha1.ValueAccessor{Read: &v1alpha1.ReadAccessor{Expression: `(object[?"spec"][?"replicatedJobs"].orValue([])).map(x, x[?"metadata"].orValue(dyn(null)))`}, Write: &v1alpha1.WriteAccessor{PathExpression: `"/spec/replicatedJobs/" + string(index) + "/metadata"`}},
						},
						ScaleDefinition: &v1alpha1.ScaleDefinition{
							Replicas: &v1alpha1.ValueAccessor{Read: &v1alpha1.ReadAccessor{Expression: `(object[?"spec"][?"replicatedJobs"].orValue([])).map(x, x[?"replicas"].orValue(dyn(null)))`}, Write: &v1alpha1.WriteAccessor{PathExpression: `"/spec/replicatedJobs/" + string(index) + "/replicas"`}},
						},
						PodSelector: &v1alpha1.PodSelector{
							ComponentTypeSelector: &v1alpha1.ComponentTypeSelector{
								Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"job-name"].orValue(dyn(null))`},
							},
							ComponentInstanceSelector: &v1alpha1.ComponentInstanceSelector{
								Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"job-name"].orValue(dyn(null))`},
							},
						},
					},
				},
			},
		},
	}
}

// NewJobGroupObject creates a test instance of JobGroup
// Array job structure with multiple discovered components via array iteration
func NewJobGroupObject() *JobGroup {
	return &JobGroup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "jobs.example.com/v1",
			Kind:       "JobGroup",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "jobgroup-example",
			Namespace: "default",
			Labels: map[string]string{
				"app":  "jobgroup",
				"type": "batch-processing",
			},
		},
		Spec: JobGroupSpec{
			ReplicatedJobs: []ReplicatedJob{
				{
					Name:     "indexer",
					Replicas: 2,
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "indexer",
								Image: "indexer:latest",
								Command: []string{
									"python",
									"/app/index.py",
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("500m"),
										corev1.ResourceMemory: resource.MustParse("1Gi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("1"),
										corev1.ResourceMemory: resource.MustParse("2Gi"),
									},
								},
							},
						},
						RestartPolicy: corev1.RestartPolicyNever,
					},
					Metadata: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":      "jobgroup",
							"job-name": "indexer",
							"role":     "indexer",
						},
					},
				},
				{
					Name:     "processor",
					Replicas: 3,
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "processor",
								Image: "processor:latest",
								Command: []string{
									"python",
									"/app/process.py",
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("1"),
										corev1.ResourceMemory: resource.MustParse("2Gi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("2"),
										corev1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
							},
						},
						RestartPolicy: corev1.RestartPolicyNever,
					},
					Metadata: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":      "jobgroup",
							"job-name": "processor",
							"role":     "processor",
						},
					},
				},
			},
		},
		Status: JobGroupStatus{
			Conditions: []metav1.Condition{
				{
					Type:   "Running",
					Status: metav1.ConditionTrue,
				},
			},
		},
	}
}
