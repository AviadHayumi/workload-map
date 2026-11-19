// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// VcJob returns the built-in Karta for the Volcano Job workload
// (batch.volcano.sh/v1alpha1).
//
// Volcano runs batch jobs with named task groups. Each task carries its own
// pod template, replica count, and optional minAvailable gang minimum. The
// controller creates pods directly (no intermediate Job object) and labels
// each pod with volcano.sh/task-spec set to the task name.
//
// Status is a phase string at .status.state.phase. The controller does not
// write observedGeneration or standard Kubernetes conditions. Mapped phases:
//   - Pending to Initializing.
//   - Restarting to Initializing when retries remain
//     (retryCount < maxRetry; Volcano defaults maxRetry to 3),
//     to Failed when exhausted.
//   - Running to Running (minAvailable tasks are scheduled).
//   - Completing to Completed (in-flight, always ends at Completed).
//   - Completed to Completed.
//   - Terminating to Failed (in-flight, ending at Terminated).
//   - Terminated and Failed to Failed.
//   - Aborting to Suspending (in-flight abort that Volcano can resume).
//   - Aborted to Suspended (resumable terminal state).
//
// Degraded is not mapped. The controller writes no fault condition or error
// reason to status; a consumer wanting pod faults reads them from pods.
//
// Unproven by recordings: Completing. The in-flight phase always ends at
// Completed but no e2e flow captures a frame in Completing alone; the
// completed flow reaches Completed directly. Writes are unproven.

func VcJob() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "batch-volcano-sh-job-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "vcjob",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "batch.volcano.sh",
					Version: "v1alpha1",
					Kind:    "Job",
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
					PhaseDefinition: &v1alpha2.PhaseDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"state"][?"phase"].orValue(dyn(null))`,
						},
					},
					StatusMappings: v1alpha2.StatusMappings{
						Initializing: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Pending",
							},
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"state"][?"phase"].orValue(dyn(null)) == "Restarting" && object[?"status"][?"retryCount"].orValue(0) < object[?"spec"][?"maxRetry"].orValue(3)`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Running",
							},
						},
						Completed: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Completing",
							},
							{
								ByPhase: "Completed",
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Terminating",
							},
							{
								ByPhase: "Terminated",
							},
							{
								ByPhase: "Failed",
							},
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"state"][?"phase"].orValue(dyn(null)) == "Restarting" && object[?"status"][?"retryCount"].orValue(0) >= object[?"spec"][?"maxRetry"].orValue(3)`,
								},
							},
						},
						Suspended: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Aborted",
							},
						},
						Suspending: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Aborting",
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "task",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Pod",
					},
					OwnerRef: ptr.To("vcjob"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `(object[?"spec"][?"tasks"].orValue([])).map(x, x[?"template"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/tasks/" + string(index) + "/template"`,
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `(object[?"spec"][?"tasks"].orValue([])).map(x, x[?"replicas"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/tasks/" + string(index) + "/replicas"`,
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `(object[?"spec"][?"tasks"].orValue([])).map(x, x[?"minAvailable"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/tasks/" + string(index) + "/minAvailable"`,
							},
						},
					},
					InstanceIds: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `(object[?"spec"][?"tasks"].orValue([])).map(x, x[?"name"].orValue(dyn(null)))`,
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentInstanceSelector: &v1alpha2.ComponentInstanceSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"volcano.sh/task-spec"].orValue(dyn(null))`,
							},
						},
					},
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{
			GangScheduling: &v1alpha2.GangSchedulingInstruction{
				PodGroups: []v1alpha2.PodGroupDefinition{
					{
						Name: "job",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "task",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"volcano.sh/job-name"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
