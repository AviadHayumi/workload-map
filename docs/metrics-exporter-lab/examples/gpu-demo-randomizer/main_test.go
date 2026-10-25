// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const testNamespace = "demo"

func gpuPod(name, uid string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: types.UID(uid), ResourceVersion: "1", Labels: map[string]string{optIn: "true"}, Annotations: map[string]string{"keep": "original"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

func metrics(uid, root string) string {
	return fmt.Sprintf(`# TYPE karta_pod_workload_info gauge
karta_pod_workload_info{namespace="demo",pod_uid=%q,workload="training",workload_uid=%q,owner_resolution="resolved"} 1
# TYPE karta_exporter_observation_healthy gauge
karta_exporter_observation_healthy{namespace="demo",scope="workload",workload_uid=%q,reason="none"} 1
`, uid, root, root)
}

func TestMappingRejectsAmbiguityAndUnhealthyRoots(t *testing.T) {
	for name, body := range map[string]string{
		"valid":     metrics("p", "r"),
		"unhealthy": metrics("p", "r") + "karta_exporter_observation_healthy{scope=\"workload\",workload_uid=\"r\",reason=\"unresolved_owner\"} 0\n",
		"ambiguous": metrics("p", "r") + "karta_exporter_observation_healthy{scope=\"workload\",workload_uid=\"other\",reason=\"none\"} 1\nkarta_pod_workload_info{namespace=\"demo\",pod_uid=\"p\",workload=\"other\",workload_uid=\"other\",owner_resolution=\"resolved\"} 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, err := parseMappings(body)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if name == "valid" {
				want = 1
			}
			if len(m) != want {
				t.Fatalf("mappings=%v", m)
			}
		})
	}
}

func TestCohortsRequireAllCurrentGPUChildrenOptedIn(t *testing.T) {
	a, b := gpuPod("a", "a-uid"), gpuPod("b", "b-uid")
	mapping := map[string]identity{"a-uid": {testNamespace, "root", "r"}, "b-uid": {testNamespace, "root", "r"}}
	for _, change := range []string{"none", "opt-out", "pending", "replacement", "other-namespace"} {
		t.Run(change, func(t *testing.T) {
			p := b.DeepCopy()
			switch change {
			case "opt-out":
				p.Labels[optIn] = "false"
			case "pending":
				p.Status.Phase = corev1.PodPending
			case "replacement":
				p.UID = "new-uid"
			case "other-namespace":
				p.Namespace = "unrelated"
			}
			c, err := selectCohorts([]corev1.Pod{a, *p}, mapping, testNamespace)
			if change == "replacement" {
				if err == nil {
					t.Fatal("replacement without a new mapping must fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if change == "none" || change == "other-namespace" {
				want = 1
			}
			if len(c) != want {
				t.Fatalf("cohorts=%v", c)
			}
		})
	}
}

func TestRoundIsDeterministicAndContainsBothScenarios(t *testing.T) {
	c := map[string]cohort{}
	for i := 0; i < 8; i++ {
		uid := fmt.Sprint(i)
		c[uid] = cohort{root: identity{testNamespace, "workload-" + uid, uid}}
	}
	at := time.Unix(1000, 0)
	a, b := newRound(c, testNamespace, at), newRound(c, testNamespace, at)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatal("same cohort, namespace and time must reproduce a round")
	}
	counts := map[string]int{}
	for _, v := range a.Assignments {
		counts[v.Scenario]++
	}
	if counts["quiet"] != 4 || counts["active"] != 4 || a.Ends.Sub(a.Started) != 10*time.Minute {
		t.Fatalf("bad stable round: %+v", a)
	}
}

func TestPatchGuardsIdentityAndPreservesOtherAnnotations(t *testing.T) {
	p := gpuPod("worker", "old-uid")
	a := assignment{Namespace: testNamespace, WorkloadUID: "r", Scenario: "quiet", Utilization: "0-3"}
	s := round{ID: "round", Started: time.Unix(1000, 0)}
	patch, err := podPatch(p, a, s)
	if err != nil {
		t.Fatal(err)
	}
	var ops []struct {
		Op, Path string
		Value    any
	}
	if err = json.Unmarshal(patch, &ops); err != nil {
		t.Fatal(err)
	}
	if ops[0].Op != "test" || ops[0].Path != "/metadata/uid" || ops[0].Value != "old-uid" || ops[1].Path != "/metadata/resourceVersion" || ops[2].Op != "test" {
		t.Fatalf("missing race guards: %s", patch)
	}
	annotations := ops[3].Value.(map[string]any)
	if annotations["keep"] != "original" || annotations[utilization] != "0-3" {
		t.Fatalf("bad annotation patch %s", patch)
	}
	p.Annotations = map[string]string{}
	for k, v := range annotations {
		p.Annotations[k] = v.(string)
	}
	patch, err = podPatch(p, a, s)
	if err != nil || patch != nil {
		t.Fatal("unchanged round must not patch again")
	}
	p.Labels[optIn] = "false"
	if _, err = podPatch(p, a, s); err == nil {
		t.Fatal("opted-out Pod accepted")
	}
}

func TestReconcileRetainsRoundAndHonorsLease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(metrics("p", "r"))) }))
	defer server.Close()
	p := gpuPod("worker", "p")
	cm := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "state", Namespace: testNamespace}}
	client := fake.NewClientset(&p, &cm)
	r := runner{client: client, namespace: testNamespace, endpoint: server.URL, configMap: "state", holder: "job-one"}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := r.reconcile(ctx, now); err != nil {
		t.Fatal(err)
	}
	get := func() round {
		t.Helper()
		x, err := client.CoreV1().ConfigMaps(testNamespace).Get(ctx, "state", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		s := round{}
		if err = json.Unmarshal([]byte(x.Data["state.json"]), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := get()
	if first.Status != "applied" || len(first.Assignments) != 1 || len(first.Assignments[0].Applied) != 1 {
		t.Fatalf("round=%+v", first)
	}
	if err := r.reconcile(ctx, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	second := get()
	if first.ID != second.ID || first.Seed != second.Seed {
		t.Fatal("minute ticks changed the round")
	}
	x, _ := client.CoreV1().ConfigMaps(testNamespace).Get(ctx, "state", metav1.GetOptions{})
	second.LeaseHolder = "another-job"
	second.LeaseUntil = now.Add(8 * time.Minute)
	body, _ := json.Marshal(second)
	x.Data["state.json"] = string(body)
	_, _ = client.CoreV1().ConfigMaps(testNamespace).Update(ctx, x, metav1.UpdateOptions{})
	before := len(client.Actions())
	if err := r.reconcile(ctx, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, a := range client.Actions()[before:] {
		if a.GetVerb() == "patch" || a.GetVerb() == "update" {
			t.Fatal("overlapping manual run mutated state")
		}
	}
	if err := r.reconcile(ctx, now.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if get().ID == first.ID {
		t.Fatal("expired round did not rotate")
	}
}

func TestSourceErrorLeavesPodsAndStateUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := fake.NewClientset()
	r := runner{client: client, namespace: testNamespace, endpoint: server.URL}
	if err := r.reconcile(context.Background(), time.Now()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("error=%v", err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("failed source led to Kubernetes mutations")
	}
}

func TestStaleUIDPatchCannotTouchReplacement(t *testing.T) {
	old := gpuPod("worker", "old-uid")
	a := assignment{Namespace: testNamespace, WorkloadUID: "root", Scenario: "quiet", Utilization: "0-3"}
	patch, err := podPatch(old, a, round{ID: "one", Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	replacement := old.DeepCopy()
	replacement.UID = "new-uid"
	client := fake.NewClientset(replacement)
	if _, err = client.CoreV1().Pods(testNamespace).Patch(context.Background(), old.Name, types.JSONPatchType, patch, metav1.PatchOptions{}); err == nil {
		t.Fatal("stale UID patch was accepted")
	}
	got, err := client.CoreV1().Pods(testNamespace).Get(context.Background(), old.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Annotations[utilization] != "" || got.UID != "new-uid" {
		t.Fatal("replacement was changed")
	}
}

func TestCASConflictDoesNotPatchPods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(metrics("p", "r"))) }))
	defer server.Close()
	p := gpuPod("worker", "p")
	cm := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "state", Namespace: testNamespace}}
	client := fake.NewClientset(&p, &cm)
	client.PrependReactor("update", "configmaps", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "state", fmt.Errorf("concurrent lease"))
	})
	r := runner{client: client, namespace: testNamespace, endpoint: server.URL, configMap: "state", holder: "job"}
	if err := r.reconcile(context.Background(), time.Now()); err == nil {
		t.Fatal("CAS conflict must fail")
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatal("Pod was patched without a lease")
		}
	}
}

func TestPartialPatchRetriesAndMissingCohortIsExplicit(t *testing.T) {
	body := metrics("p", "r") + "karta_exporter_observation_healthy{scope=\"workload\",workload_uid=\"s\",reason=\"none\"} 1\nkarta_pod_workload_info{namespace=\"demo\",pod_uid=\"q\",workload=\"second\",workload_uid=\"s\",owner_resolution=\"resolved\"} 1\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	p, q := gpuPod("first", "p"), gpuPod("second", "q")
	cm := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "state", Namespace: testNamespace}}
	client := fake.NewClientset(&p, &q, &cm)
	fail := true
	client.PrependReactor("patch", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		if fail && a.(ktesting.PatchAction).GetName() == "first" {
			return true, nil, fmt.Errorf("temporary write failure")
		}
		return false, nil, nil
	})
	r := runner{client: client, namespace: testNamespace, endpoint: server.URL, configMap: "state", holder: "job"}
	ctx := context.Background()
	now := time.Now().UTC()
	read := func() round {
		t.Helper()
		x, err := client.CoreV1().ConfigMaps(testNamespace).Get(ctx, "state", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		s := round{}
		if err = json.Unmarshal([]byte(x.Data["state.json"]), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if err := r.reconcile(ctx, now); err == nil {
		t.Fatal("patch failure hidden")
	}
	first := read()
	if first.Status != "partial" {
		t.Fatalf("status=%s", first.Status)
	}
	fail = false
	if err := r.reconcile(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	recovered := read()
	if recovered.ID != first.ID || recovered.Status != "applied" {
		t.Fatalf("bad retry %+v", recovered)
	}
	if err := client.CoreV1().Pods(testNamespace).Delete(ctx, "first", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcile(ctx, now.Add(2*time.Minute)); err == nil {
		t.Fatal("missing cohort hidden")
	}
	missing := read()
	if missing.Status != "partial" {
		t.Fatalf("status=%s", missing.Status)
	}
	for _, a := range missing.Assignments {
		if a.WorkloadUID == "r" && (a.Status != "blocked" || a.Reason != "cohort_unavailable" || len(a.Applied) != 0) {
			t.Fatalf("bad missing assignment %+v", a)
		}
	}
}
