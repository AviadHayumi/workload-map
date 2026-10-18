// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package flows

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/test/e2e/recorder"
)

// operatorCurrentObservation excludes a known old observation without requiring a field
// that some operator versions omit.
func operatorCurrentObservation(u *unstructured.Unstructured) bool {
	generation, hasGeneration, _ := unstructured.NestedInt64(u.Object, "metadata", "generation")
	observed, hasObserved, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	return !hasGeneration || !hasObserved || observed >= generation
}

func operatorConditionReason(condType, status string, reasons ...string) recorder.StateCheck {
	return func(u *unstructured.Unstructured) bool {
		conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, condition := range conditions {
			c, ok := condition.(map[string]any)
			if !ok || (condType != "" && c["type"] != condType) || (status != "" && c["status"] != status) {
				continue
			}
			for _, reason := range reasons {
				if c["reason"] == reason {
					return true
				}
			}
		}
		return false
	}
}

func RayClusterPending() recorder.StateCheck {
	return func(u *unstructured.Unstructured) bool {
		if BoolTrue("spec", "suspend")(u) || PhaseAny([]string{"failed", "suspended"}, "status", "state")(u) ||
			CondTrue("RayClusterSuspending", "RayClusterSuspended")(u) {
			return false
		}
		return CondFalse("RayClusterProvisioned")(u) && CondFalse("HeadPodReady")(u)
	}
}

func RayClusterProgressing() recorder.StateCheck {
	return AllOf(RayClusterPending(), operatorConditionReason("HeadPodReady", "False",
		"ContainerCreating", "ContainersNotReady", "ErrImagePull", "ImagePullBackOff", "CrashLoopBackOff"))
}

func rayJobCanStart(u *unstructured.Unstructured) bool {
	if BoolTrue("spec", "suspend")(u) || PhaseAny([]string{"RUNNING", "SUCCEEDED", "FAILED", "STOPPED"}, "status", "jobStatus")(u) {
		return false
	}
	deployment, _, _ := unstructured.NestedString(u.Object, "status", "jobDeploymentStatus")
	switch deployment {
	case "", "Initializing", "Running", "Waiting":
		return true
	default:
		return false
	}
}

func RayJobPending() recorder.StateCheck {
	return func(u *unstructured.Unstructured) bool {
		return rayJobCanStart(u) && (PhaseEq("PENDING", "status", "jobStatus")(u) ||
			PhaseAny([]string{"Initializing", "Waiting"}, "status", "jobDeploymentStatus")(u))
	}
}

func RayJobProgressing() recorder.StateCheck {
	return AllOf(rayJobCanStart, PhaseEq("Initializing", "status", "jobDeploymentStatus"))
}

func DynamoPending() recorder.StateCheck {
	return AllOf(operatorCurrentObservation, PhaseEq("pending", "status", "state"))
}

func DynamoProgressing() recorder.StateCheck {
	return func(u *unstructured.Unstructured) bool {
		return operatorCurrentObservation(u) && !PhaseEq("failed", "status", "state")(u) &&
			PhaseAny([]string{"Pending", "InProgress"}, "status", "rollingUpdate", "phase")(u)
	}
}

func KnativePending() recorder.StateCheck {
	return AllOf(operatorCurrentObservation, CondStatus("Ready", "Unknown"))
}

func KnativeProgressing() recorder.StateCheck {
	return func(u *unstructured.Unstructured) bool {
		if !KnativePending()(u) {
			return false
		}
		created, _, _ := unstructured.NestedString(u.Object, "status", "latestCreatedRevisionName")
		ready, _, _ := unstructured.NestedString(u.Object, "status", "latestReadyRevisionName")
		return (created != "" && created != ready) ||
			operatorConditionReason("Ready", "Unknown", "OutOfDate", "TrafficNotMigrated")(u)
	}
}

func milvusCanTransition(u *unstructured.Unstructured) bool {
	return operatorCurrentObservation(u) && !PhaseEq("Stopped", "status", "status")(u) &&
		!operatorConditionReason("", "", "MilvusStopped", "MilvusStopping")(u)
}

func MilvusPending() recorder.StateCheck {
	return AllOf(milvusCanTransition, PhaseEq("Pending", "status", "status"))
}

func MilvusProgressing() recorder.StateCheck {
	return AllOf(milvusCanTransition, operatorConditionReason("MilvusUpdated", "False",
		"MilvusUpgradingImage", "MilvusDowngradingImage", "MilvusComponentsUpdating"))
}

func TrainingPending() recorder.StateCheck {
	return func(u *unstructured.Unstructured) bool {
		return !BoolTrue("spec", "runPolicy", "suspend")(u) && CondTrue("Created")(u) &&
			!CondTrue("Running", "Succeeded", "Failed", "Suspended")(u)
	}
}

func TrainingProgressing() recorder.StateCheck {
	return func(u *unstructured.Unstructured) bool {
		if !TrainingPending()(u) {
			return false
		}
		replicas, _, _ := unstructured.NestedMap(u.Object, "status", "replicaStatuses")
		for _, replica := range replicas {
			if fields, ok := replica.(map[string]any); ok {
				if active, _, _ := unstructured.NestedInt64(fields, "active"); active > 0 {
					return true
				}
			}
		}
		return false
	}
}
