// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package flows

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func inferenceObservationCurrent(u *unstructured.Unstructured) bool {
	observed, found, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	return !found || u.GetGeneration() == 0 || observed >= u.GetGeneration()
}

func nimPending(u *unstructured.Unstructured) bool {
	replicas, found, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	return inferenceObservationCurrent(u) && (!found || replicas > 0) && PhaseAny([]string{"Pending", "NotReady", "PVC-Created"}, "status", "state")(u)
}

func grovePending(u *unstructured.Unstructured) bool {
	available, hasAvailable, _ := unstructured.NestedInt64(u.Object, "status", "availableReplicas")
	_, observed, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	return inferenceObservationCurrent(u) && !CondTrue("TopologyLevelsUnavailable")(u) && (hasAvailable || observed) && desired > 0 && available < desired
}

func groveProgressing(u *unstructured.Unstructured) bool {
	return inferenceObservationCurrent(u) && !CondTrue("TopologyLevelsUnavailable")(u) &&
		(CondTrue("UpdateInProgress")(u) || operatorConditionReason("UpdateInProgress", "Unknown", "ProgressDeadlineExceeded")(u))
}

func groveRunning(u *unstructured.Unstructured) bool {
	status, hasStatus, _ := unstructured.NestedMap(u.Object, "status")
	available, _, _ := unstructured.NestedInt64(status, "availableReplicas")
	desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	return inferenceObservationCurrent(u) && hasStatus && available >= desired
}

func sandboxPending(u *unstructured.Unstructured) bool {
	return inferenceObservationCurrent(u) && inferenceConditionsCurrent(u, "Ready", "Suspended") && SandboxMode("Running")(u) &&
		operatorConditionReason("Ready", "False", "DependenciesNotReady")(u) &&
		CondFalse("Suspended")(u) && !CondTrue("Finished")(u)
}

func sandboxProgressing(u *unstructured.Unstructured) bool {
	return sandboxPending(u) && inferenceConditionsCurrent(u, "PodScheduled") && CondTrue("PodScheduled")(u)
}

func inferenceConditionsCurrent(u *unstructured.Unstructured, conditionTypes ...string) bool {
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _, _ := unstructured.NestedString(condition, "type")
		observed, found, _ := unstructured.NestedInt64(condition, "observedGeneration")
		if slices.Contains(conditionTypes, conditionType) && found && observed < u.GetGeneration() {
			return false
		}
	}
	return true
}

func inferenceServiceProgressing(u *unstructured.Unstructured) bool {
	return inferenceObservationCurrent(u) && !CondsFalse("PredictorReady", "PredictorConfigurationReady", "RoutesReady")(u) &&
		PhaseEq("Loading", "status", "modelStatus", "states", "targetModelState")(u)
}

func inferenceServicePending(u *unstructured.Unstructured) bool {
	if !inferenceObservationCurrent(u) || CondTrue("Ready")(u) || CondsFalse("PredictorReady", "PredictorConfigurationReady", "RoutesReady")(u) {
		return false
	}
	for _, name := range []string{"Ready", "PredictorReady", "RoutesReady", "LatestDeploymentReady"} {
		if CondStatus(name, "Unknown")(u) {
			return true
		}
	}
	return inferenceServiceProgressing(u) && operatorConditionReason("PredictorReady", "False", "MinimumReplicasUnavailable")(u)
}
