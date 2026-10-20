// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import "github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"

const (
	MetricPodWorkloadInfo          = "karta_pod_workload_info"
	MetricWorkloadStatus           = "karta_workload_status"
	MetricWorkloadCreatedTimestamp = "karta_workload_created_timestamp_seconds"
	MetricExporterObservationValid = "karta_exporter_observation_valid"
)

const (
	LabelNamespace         = "namespace"
	LabelPod               = "pod_name"
	LabelPodUID            = "pod_uid"
	LabelWorkload          = "workload_name"
	LabelWorkloadUID       = "workload_uid"
	LabelWorkloadKind      = "workload_kind"
	LabelWorkloadGroup     = "workload_group"
	LabelWorkloadVersion   = "workload_version"
	LabelOwnerResolution   = "owner_resolution"
	LabelKarta             = "karta"
	LabelKartaSource       = "karta_source"
	LabelComponent         = "component"
	LabelComponentInstance = "component_instance"
	LabelReplicaGroupID    = "replica_group_id"
	LabelPhase             = "phase"
	LabelReason            = "reason"
	LabelSubmittedBy       = "submitted_by"
	LabelObservationValid  = "observation_valid"
	LabelResourceGroup     = "resource_group"
	LabelResourceVersion   = "resource_version"
	LabelResourceKind      = "resource_kind"
)

// SentinelUnknown cannot collide with a Kubernetes object name.
const SentinelUnknown = "<unknown>"

// Internal attribution failures are mapped to observation-health reasons.
const (
	ReasonUnknownInstance = "unknown_instance"
	ReasonJQError         = "jq_error"
)

// AllPhases contains the normalized phases emitted as dense 0/1 observations.
var AllPhases = []v1alpha1.ResourceStatus{
	v1alpha1.InitializingStatus,
	v1alpha1.RunningStatus,
	v1alpha1.CompletedStatus,
	v1alpha1.FailedStatus,
	v1alpha1.DegradedStatus,
	v1alpha1.SuspendedStatus,
	v1alpha1.SuspendingStatus,
	v1alpha1.ResumingStatus,
	v1alpha1.UndefinedStatus,
}
