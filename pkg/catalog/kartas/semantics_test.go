// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas_test

import (
	"encoding/json"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

func component(t *testing.T, karta *v1alpha1.Karta, object, name string) *resource.Component {
	t.Helper()
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(object), &obj.Object); err != nil {
		t.Fatal(err)
	}
	c, err := resource.NewComponentFactoryFromObject(karta, obj).GetComponent(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOptionalGroveScalingGroups(t *testing.T) {
	for _, groups := range []string{"", `,"podCliqueScalingGroups":[]`, `,"podCliqueScalingGroups":null`, `,"podCliqueScalingGroups":[{"name":"workers","replicas":3}]`} {
		t.Run(groups, func(t *testing.T) {
			obj := `{"spec":{"replicas":2,"template":{"cliques":[]` + groups + `}}}`
			c := component(t, kartas.GrovePodCliqueSet(), obj, "scalinggroup")
			ids, err := c.GetInstanceIds(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			scales, err := c.GetScale(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) == 0 {
				if len(scales) != 0 {
					t.Fatalf("empty groups have scales: %v", scales)
				}
				return
			}
			if len(ids) != 1 || ids[0] != "workers" || scales["workers"].Replicas == nil || *scales["workers"].Replicas != 6 {
				t.Fatalf("ids/scales = %v/%v, want workers=6", ids, scales)
			}
		})
	}
}

func TestGroveCliqueInstanceIdentity(t *testing.T) {
	selector := kartas.GrovePodCliqueSet().Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentInstanceSelector
	for _, tc := range []struct {
		name, clique, root, replica, want string
	}{
		{"generated name", "training-0-worker", "training", "0", "worker"},
		{"hyphenated names", "training-job-1-gpu-workers", "training-job", "1", "gpu-workers"},
		{"template label", "worker", "training", "0", "worker"},
		{"no prefix context", "worker", "", "", "worker"},
		{"mismatched prefix", "other-0-worker", "training", "0", "other-0-worker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
				"grove.io/podclique": tc.clique, "app.kubernetes.io/part-of": tc.root,
				"grove.io/podcliqueset-replica-index": tc.replica,
			}}}
			got, found, err := resource.NewPodQuerier(pod).ExtractInstanceId(t.Context(), selector)
			if err != nil || !found || got != tc.want {
				t.Fatalf("instance = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestKnativeScaleAnnotations(t *testing.T) {
	for _, tc := range []struct {
		name, annotations string
		min               int32
		max               *int32
		wantError         bool
	}{
		{name: "omitted", annotations: `{}`, min: 0},
		{name: "numeric strings", annotations: `{"autoscaling.knative.dev/min-scale":"2","autoscaling.knative.dev/max-scale":"5"}`, min: 2, max: ptr.To(int32(5))},
		{name: "zero", annotations: `{"autoscaling.knative.dev/min-scale":"0"}`, min: 0},
		{name: "invalid", annotations: `{"autoscaling.knative.dev/min-scale":"bad"}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := component(t, kartas.KnativeServing(), `{"spec":{"template":{"metadata":{"annotations":`+tc.annotations+`}}}}`, "revision")
			scales, err := c.GetScale(t.Context())
			if tc.wantError {
				if err == nil {
					t.Fatal("invalid annotation accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			scale := scales[""]
			if scale.MinReplicas == nil || *scale.MinReplicas != tc.min || !ptr.Equal(scale.MaxReplicas, tc.max) {
				t.Fatalf("scale = %+v, want min=%d max=%v", scale, tc.min, tc.max)
			}
		})
	}
}

func TestKServeReadyAcrossDeploymentModes(t *testing.T) {
	for _, tc := range []struct {
		name, conditions string
		running          bool
	}{
		{"raw", `[{"type":"Ready","status":"True"},{"type":"PredictorReady","status":"True"},{"type":"IngressReady","status":"True"}]`, true},
		{"serverless", `[{"type":"Ready","status":"True"},{"type":"PredictorReady","status":"True"},{"type":"RoutesReady","status":"True"},{"type":"LatestDeploymentReady","status":"True"}]`, true},
		{"transformer not ready", `[{"type":"Ready","status":"False"},{"type":"PredictorReady","status":"True"},{"type":"RoutesReady","status":"True"},{"type":"LatestDeploymentReady","status":"True"}]`, false},
		{"unknown", `[{"type":"Ready","status":"Unknown"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := component(t, kartas.KServe(), `{"status":{"conditions":`+tc.conditions+`}}`, "inferenceservice")
			status, err := c.GetStatus(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(status.MatchedStatuses, v1alpha1.RunningStatus); got != tc.running {
				t.Fatalf("matched phases = %v, want Running=%t", status.MatchedStatuses, tc.running)
			}
		})
	}
}

func TestDynamoReplicaApplicability(t *testing.T) {
	for _, k := range []*v1alpha1.Karta{kartas.Dynamo(), kartas.DynamoV1beta1()} {
		t.Run(k.Name, func(t *testing.T) {
			selector := k.Spec.StructureDefinition.ChildComponents[0].PodSelector.ReplicaSelector
			for _, tc := range []struct {
				name, owner string
				labels      map[string]string
				want        string
				wantError   bool
			}{
				{name: "deployment", owner: "ReplicaSet"},
				{name: "deployment with inherited group label", owner: "ReplicaSet", labels: map[string]string{"grove.io/podcliqueset-replica-index": "0"}},
				{name: "lws", owner: "StatefulSet", labels: map[string]string{"leaderworkerset.sigs.k8s.io/group-index": "1"}, want: "1"},
				{name: "grove scaling group", owner: "PodClique", labels: map[string]string{"grove.io/podcliquescalinggroup-replica-index": "2"}, want: "2"},
				{name: "grove clique set", owner: "PodClique", labels: map[string]string{"grove.io/podcliqueset-replica-index": "0"}, want: "0"},
				{name: "grove scaling group within clique set", owner: "PodClique", labels: map[string]string{"grove.io/podcliquescalinggroup-replica-index": "2", "grove.io/podcliqueset-replica-index": "0"}, want: "2"},
				{name: "missing grouped key", owner: "PodClique", wantError: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: tc.labels, OwnerReferences: []metav1.OwnerReference{{Kind: tc.owner, Controller: ptr.To(true)}}}}
					got, found, err := resource.NewPodQuerier(pod).ExtractReplicaKey(t.Context(), selector)
					if (err != nil) != tc.wantError || got != tc.want || found != (tc.want != "") {
						t.Fatalf("replica = %q,%t,%v, want %q/error=%t", got, found, err, tc.want, tc.wantError)
					}
				})
			}
		})
	}
}

func TestJobSetRepeatedJobIdentity(t *testing.T) {
	selector := kartas.Jobset().Spec.StructureDefinition.ChildComponents[0].PodSelector.ReplicaSelector
	for _, index := range []string{"0", "1"} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"jobset.sigs.k8s.io/job-index": index}}}
		got, found, err := resource.NewPodQuerier(pod).ExtractReplicaKey(t.Context(), selector)
		if err != nil || !found || got != index {
			t.Fatalf("replica = %q,%t,%v, want %q", got, found, err, index)
		}
	}
}

func TestRayJobSubmitterComponent(t *testing.T) {
	summary, err := instructions.NewStructureSummary(kartas.Rayjob())
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"batch.kubernetes.io/job-name": "example"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Controller: ptr.To(true)}}}}
	got, err := instructions.InferPodComponent(t.Context(), resource.NewPodQuerier(pod), summary)
	if err != nil || got != "submitter" {
		t.Fatalf("component = %q,%v, want submitter", got, err)
	}
}
