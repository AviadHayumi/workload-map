// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package flows

import (
	"testing"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/json"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/test/e2e/recorder"
)

func TestBuiltinFlowPredicates(t *testing.T) {
	checks := map[string]map[v1alpha1.ResourceStatus]recorder.StateCheck{
		"Pod": {
			v1alpha1.PendingStatus:     PhaseEq("Pending", "status", "phase"),
			v1alpha1.ProgressingStatus: PodProgressing(),
			v1alpha1.RunningStatus:     PhaseEq("Running", "status", "phase"),
			v1alpha1.CompletedStatus:   PhaseEq("Succeeded", "status", "phase"),
			v1alpha1.FailedStatus:      PhaseEq("Failed", "status", "phase"),
		},
		"Deployment": {
			v1alpha1.PendingStatus:     DeploymentPending(),
			v1alpha1.ProgressingStatus: DeploymentProgressing(),
			v1alpha1.RunningStatus:     DeploymentRunning(),
			v1alpha1.FailedStatus:      DeploymentFailed(),
		},
		"StatefulSet": {
			v1alpha1.PendingStatus:     ReplicasPending(),
			v1alpha1.ProgressingStatus: ReplicasProgressing(),
			v1alpha1.RunningStatus:     ReplicasRunning(),
			v1alpha1.DegradedStatus:    ReplicasDegraded(),
		},
		"CronJob": {
			v1alpha1.PendingStatus:   CronjobPending(),
			v1alpha1.RunningStatus:   CronjobFired(),
			v1alpha1.SuspendedStatus: BoolTrue("spec", "suspend"),
		},
		"BatchJob": {
			v1alpha1.PendingStatus:   JobPending(),
			v1alpha1.RunningStatus:   JobRunning(),
			v1alpha1.CompletedStatus: CondTrue("Complete", "SuccessCriteriaMet"),
			v1alpha1.FailedStatus:    CondTrue("Failed", "FailureTarget"),
			v1alpha1.SuspendedStatus: CondTrue("Suspended"),
			v1alpha1.DegradedStatus:  JobDegraded(),
		},
		"Jobset": {
			v1alpha1.PendingStatus:     JobsetPending(),
			v1alpha1.ProgressingStatus: JobsetProgressing(),
			v1alpha1.RunningStatus:     JobsetRunning(),
			v1alpha1.CompletedStatus:   CondTrue("Completed"),
			v1alpha1.FailedStatus:      CondTrue("Failed"),
			v1alpha1.SuspendedStatus:   JobsetSuspended(),
		},
		"LWS": {
			v1alpha1.PendingStatus:     LWSPending(),
			v1alpha1.ProgressingStatus: LWSProgressing(),
			v1alpha1.RunningStatus:     LWSRunning(),
			v1alpha1.FailedStatus:      LWSFailed(),
		},
	}
	tests := []struct {
		name   string
		kind   string
		object string
		want   []v1alpha1.ResourceStatus
	}{
		{
			name:   "Pod/absent status",
			kind:   "Pod",
			object: `{"metadata":{"generation":1},"spec":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "Deployment/absent status",
			kind:   "Deployment",
			object: `{"metadata":{"generation":1},"spec":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "StatefulSet/absent status",
			kind:   "StatefulSet",
			object: `{"metadata":{"generation":1},"spec":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "CronJob/absent status",
			kind:   "CronJob",
			object: `{"metadata":{"generation":1},"spec":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "BatchJob/absent status",
			kind:   "BatchJob",
			object: `{"metadata":{"generation":1},"spec":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "Jobset/absent status",
			kind:   "Jobset",
			object: `{"metadata":{"generation":1},"spec":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "LWS/absent status",
			kind:   "LWS",
			object: `{"metadata":{"generation":1},"spec":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "Pod/unscheduled",
			kind:   "Pod",
			object: `{"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus},
		},
		{
			name:   "Pod/scheduled image pull waiting",
			kind:   "Pod",
			object: `{"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"True"}],"containerStatuses":[{"state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.ProgressingStatus},
		},
		{
			name:   "Pod/deleting pending pod",
			kind:   "Pod",
			object: `{"metadata":{"deletionTimestamp":"2026-01-01T00:00:00Z"},"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus},
		},
		{
			name:   "Deployment/startup",
			kind:   "Deployment",
			object: `{"metadata":{"generation":1},"spec":{"replicas":1,"paused":false},"status":{"observedGeneration":1,"replicas":0,"updatedReplicas":0,"availableReplicas":0,"conditions":[{"type":"Progressing","status":"True","reason":"NewReplicaSetCreated"},{"type":"Available","status":"False"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.ProgressingStatus},
		},
		{
			name:   "Deployment/blocked rollout",
			kind:   "Deployment",
			object: `{"metadata":{"generation":1},"spec":{"replicas":1,"paused":false},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"availableReplicas":0,"conditions":[{"type":"Progressing","status":"False","reason":"ProgressDeadlineExceeded"},{"type":"Available","status":"False"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.ProgressingStatus, v1alpha1.FailedStatus},
		},
		{
			name:   "Deployment/settled sticky success",
			kind:   "Deployment",
			object: `{"metadata":{"generation":1},"spec":{"replicas":1,"paused":false},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"availableReplicas":1,"conditions":[{"type":"Progressing","status":"True","reason":"NewReplicaSetAvailable"},{"type":"Available","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.RunningStatus},
		},
		{
			name:   "Deployment/partial capacity rollout",
			kind:   "Deployment",
			object: `{"metadata":{"generation":1},"spec":{"replicas":2,"paused":false},"status":{"observedGeneration":1,"replicas":2,"updatedReplicas":1,"availableReplicas":1,"conditions":[{"type":"Progressing","status":"True","reason":"ReplicaSetUpdated"},{"type":"Available","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.RunningStatus, v1alpha1.ProgressingStatus},
		},
		{
			name:   "Deployment/availability lost after success",
			kind:   "Deployment",
			object: `{"metadata":{"generation":1},"spec":{"replicas":1,"paused":false},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"availableReplicas":0,"conditions":[{"type":"Progressing","status":"True","reason":"NewReplicaSetAvailable"},{"type":"Available","status":"False"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus},
		},
		{
			name:   "Deployment/stale generation",
			kind:   "Deployment",
			object: `{"metadata":{"generation":1},"spec":{"replicas":1,"paused":false},"status":{"observedGeneration":0,"replicas":1,"updatedReplicas":1,"availableReplicas":1,"conditions":[{"type":"Progressing","status":"True","reason":"NewReplicaSetAvailable"},{"type":"Available","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "Deployment/scale to zero draining",
			kind:   "Deployment",
			object: `{"metadata":{"generation":1},"spec":{"replicas":0,"paused":false},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"availableReplicas":1,"conditions":[{"type":"Progressing","status":"True","reason":"NewReplicaSetAvailable"},{"type":"Available","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.ProgressingStatus},
		},
		{
			name:   "StatefulSet/startup readiness only",
			kind:   "StatefulSet",
			object: `{"metadata":{"generation":1},"spec":{"replicas":1},"status":{"observedGeneration":1,"replicas":1,"readyReplicas":0,"updatedReplicas":1,"currentRevision":"a","updateRevision":"a"}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus},
		},
		{
			name:   "StatefulSet/healthy rolling update",
			kind:   "StatefulSet",
			object: `{"metadata":{"generation":1},"spec":{"replicas":2},"status":{"observedGeneration":1,"replicas":2,"readyReplicas":2,"updatedReplicas":1,"currentRevision":"a","updateRevision":"b"}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.RunningStatus, v1alpha1.ProgressingStatus},
		},
		{
			name:   "StatefulSet/readiness regression",
			kind:   "StatefulSet",
			object: `{"metadata":{"generation":1},"spec":{"replicas":2},"status":{"observedGeneration":1,"replicas":2,"readyReplicas":1,"updatedReplicas":2,"currentRevision":"a","updateRevision":"a"}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.DegradedStatus},
		},
		{
			name:   "StatefulSet/partition fulfilled",
			kind:   "StatefulSet",
			object: `{"metadata":{"generation":1},"spec":{"replicas":3,"updateStrategy":{"type":"RollingUpdate","rollingUpdate":{"partition":2}}},"status":{"observedGeneration":1,"replicas":3,"readyReplicas":3,"updatedReplicas":1,"currentRevision":"a","updateRevision":"b"}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.RunningStatus},
		},
		{
			name:   "StatefulSet/OnDelete awaits explicit deletion",
			kind:   "StatefulSet",
			object: `{"metadata":{"generation":1},"spec":{"replicas":1,"updateStrategy":{"type":"OnDelete"}},"status":{"observedGeneration":1,"replicas":1,"readyReplicas":1,"updatedReplicas":0,"currentRevision":"a","updateRevision":"b"}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.RunningStatus},
		},
		{
			name:   "StatefulSet/stale generation",
			kind:   "StatefulSet",
			object: `{"metadata":{"generation":1},"spec":{"replicas":1},"status":{"observedGeneration":0,"replicas":1,"readyReplicas":1,"updatedReplicas":1,"currentRevision":"a","updateRevision":"a"}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "CronJob/waiting first schedule",
			kind:   "CronJob",
			object: `{"status":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus},
		},
		{
			name:   "CronJob/idle established schedule",
			kind:   "CronJob",
			object: `{"status":{"lastScheduleTime":"2026-01-01T00:00:00Z","active":[]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.RunningStatus},
		},
		{
			name:   "CronJob/suspended before first schedule",
			kind:   "CronJob",
			object: `{"spec":{"suspend":true},"status":{}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.SuspendedStatus},
		},
		{
			name:   "BatchJob/active without readiness field",
			kind:   "BatchJob",
			object: `{"spec":{"parallelism":1},"status":{"active":1,"conditions":[]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "BatchJob/partially ready",
			kind:   "BatchJob",
			object: `{"spec":{"parallelism":2},"status":{"active":2,"conditions":[],"ready":1}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.RunningStatus, v1alpha1.PendingStatus},
		},
		{
			name:   "BatchJob/Complete suppresses active counters",
			kind:   "BatchJob",
			object: `{"spec":{"parallelism":2},"status":{"active":2,"conditions":[{"type":"Complete","status":"True"}],"ready":1}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.CompletedStatus},
		},
		{
			name:   "BatchJob/retry with ready peer",
			kind:   "BatchJob",
			object: `{"spec":{"parallelism":2},"status":{"active":2,"ready":1,"failed":1}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.RunningStatus, v1alpha1.DegradedStatus},
		},
		{
			name:   "Jobset/active jobs are not ready",
			kind:   "Jobset",
			object: `{"spec":{"suspend":false,"replicatedJobs":[{"name":"workers","replicas":1}]},"status":{"replicatedJobsStatus":[{"name":"workers","active":1,"ready":0,"succeeded":0,"failed":0}],"conditions":[]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus},
		},
		{
			name:   "Jobset/dependent jobs not created",
			kind:   "Jobset",
			object: `{"spec":{"suspend":false,"replicatedJobs":[{"name":"workers","replicas":1}]},"status":{"replicatedJobsStatus":[{"name":"workers","active":0,"ready":0,"succeeded":0,"failed":0}],"conditions":[]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus},
		},
		{
			name:   "Jobset/ordered startup blocked",
			kind:   "Jobset",
			object: `{"spec":{"suspend":false,"replicatedJobs":[{"name":"workers","replicas":1}],"startupPolicy":{"startupPolicyOrder":"InOrder"}},"status":{"replicatedJobsStatus":[{"name":"workers","active":1,"ready":0,"succeeded":0,"failed":0}],"conditions":[{"type":"StartupPolicyInProgress","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.ProgressingStatus},
		},
		{
			name:   "Jobset/ready jobs",
			kind:   "Jobset",
			object: `{"spec":{"suspend":false,"replicatedJobs":[{"name":"workers","replicas":1}]},"status":{"replicatedJobsStatus":[{"name":"workers","active":1,"ready":1,"succeeded":0,"failed":0}],"conditions":[]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.RunningStatus},
		},
		{
			name:   "Jobset/completed guards startup condition",
			kind:   "Jobset",
			object: `{"spec":{"suspend":false,"replicatedJobs":[{"name":"workers","replicas":1}],"startupPolicy":{"startupPolicyOrder":"InOrder"}},"status":{"replicatedJobsStatus":[{"name":"workers","active":1,"ready":1,"succeeded":0,"failed":0}],"conditions":[{"type":"Completed","status":"True"},{"type":"StartupPolicyInProgress","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.CompletedStatus},
		},
		{
			name:   "Jobset/resumed ignores stale suspended condition",
			kind:   "Jobset",
			object: `{"spec":{"suspend":false,"replicatedJobs":[{"name":"workers","replicas":1}],"startupPolicy":{"startupPolicyOrder":"InOrder"}},"status":{"replicatedJobsStatus":[{"name":"workers","active":1,"ready":0,"succeeded":0,"failed":0}],"conditions":[{"type":"Suspended","status":"True"},{"type":"StartupPolicyInProgress","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.ProgressingStatus},
		},
		{
			name:   "LWS/startup",
			kind:   "LWS",
			object: `{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"conditions":[{"type":"Progressing","status":"True"}]}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.PendingStatus, v1alpha1.ProgressingStatus},
		},
		{
			name:   "LWS/healthy update",
			kind:   "LWS",
			object: `{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"conditions":[{"type":"Available","status":"False"},{"type":"Progressing","status":"False"},{"type":"UpdateInProgress","status":"True"}],"replicas":1,"readyReplicas":1,"updatedReplicas":0}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.RunningStatus, v1alpha1.ProgressingStatus},
		},
		{
			name:   "LWS/stale counts during scale up",
			kind:   "LWS",
			object: `{"metadata":{"generation":2},"spec":{"replicas":2},"status":{"conditions":[{"type":"Progressing","status":"True"}],"replicas":1,"readyReplicas":1,"updatedReplicas":1}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.ProgressingStatus},
		},
		{
			name:   "LWS/explicit stale generation",
			kind:   "LWS",
			object: `{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"conditions":[{"type":"Available","status":"True"},{"type":"UpdateInProgress","status":"True"}],"replicas":1,"readyReplicas":1,"updatedReplicas":1,"observedGeneration":1}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus},
		},
		{
			name:   "LWS/zero scale transition",
			kind:   "LWS",
			object: `{"metadata":{"generation":2},"spec":{"replicas":0},"status":{"conditions":[{"type":"Progressing","status":"True"}],"replicas":1,"readyReplicas":1}}`,
			want:   []v1alpha1.ResourceStatus{v1alpha1.ProgressingStatus},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			object := &unstructured.Unstructured{}
			g.Expect(json.Unmarshal([]byte(tt.object), &object.Object)).To(gomega.Succeed())
			var matched []v1alpha1.ResourceStatus
			for status, check := range checks[tt.kind] {
				if check(object) {
					matched = append(matched, status)
				}
			}
			if len(matched) == 0 {
				matched = []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus}
			}
			g.Expect(matched).To(gomega.ConsistOf(tt.want))
		})
	}
}
