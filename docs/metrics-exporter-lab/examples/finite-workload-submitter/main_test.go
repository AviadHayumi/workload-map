// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func testRunner(t *testing.T) (*runner, *corev1.ConfigMap, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "state", Namespace: "finite-demo", UID: "state-uid", ResourceVersion: "1"}}
	lists := map[schema.GroupVersionResource]string{}
	for _, w := range workloadTypes {
		lists[w.resource] = w.kind + "List"
	}
	d := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists)
	now := time.Unix(600*1000, 0).UTC()
	return &runner{kube: kubefake.NewSimpleClientset(cm), dynamic: d, namespace: cm.Namespace, configMap: cm.Name, holder: "runner-uid", image: "busybox@sha256:fixture", now: func() time.Time { return now }}, cm, d
}

func completed(obj *unstructured.Unstructured, at time.Time) {
	obj.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Succeeded", "status": "True", "lastTransitionTime": at.Format(time.RFC3339Nano)}}}
}

func addRoot(t *testing.T, r *runner, d *dynamicfake.FakeDynamicClient, cm *corev1.ConfigMap, w workloadType, slot int64, finish time.Time) *unstructured.Unstructured {
	t.Helper()
	obj := r.fixture(w, slot, cm)
	obj.SetUID(types.UID(obj.GetName() + "-uid"))
	obj.SetResourceVersion("7")
	if !finish.IsZero() {
		completed(obj, finish)
	}
	if err := d.Tracker().Add(obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestSlotRetryAndAlternatingKinds(t *testing.T) {
	r, cm, d := testRunner(t)
	ctx := context.Background()
	if err := r.tick(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := d.Resource(workloadTypes[0].resource).Namespace(cm.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("first submission: %v %v", first, err)
	}
	if err := r.tick(ctx); err != nil {
		t.Fatal(err)
	}
	completed(&first.Items[0], r.now())
	if _, err := d.Resource(workloadTypes[0].resource).Namespace(cm.Namespace).UpdateStatus(ctx, &first.Items[0], metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.tick(ctx); err != nil {
		t.Fatal(err)
	}
	next := r.now().Add(cadence)
	r.now = func() time.Time { return next }
	if err := r.tick(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := d.Resource(workloadTypes[1].resource).Namespace(cm.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(second.Items) != 1 {
		t.Fatalf("next kind: %v %v", second, err)
	}
	first, _ = d.Resource(workloadTypes[0].resource).Namespace(cm.Namespace).List(ctx, metav1.ListOptions{})
	if len(first.Items) != 1 {
		t.Fatal("retry duplicated first slot")
	}
}

func TestOverlapLeaseAndCASConflict(t *testing.T) {
	for _, scenario := range []string{"held", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			r, cm, d := testRunner(t)
			k := r.kube.(*kubefake.Clientset)
			if scenario == "held" {
				cm.Annotations = map[string]string{leaseHolder: "other", leaseUntil: r.now().Add(time.Minute).Format(time.RFC3339Nano)}
				if _, err := k.CoreV1().ConfigMaps(cm.Namespace).Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			} else {
				k.PrependReactor("update", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cm.Name, errors.New("concurrent acquisition"))
				})
			}
			err := r.tick(context.Background())
			if scenario == "conflict" && err == nil {
				t.Fatal("conflict must fail")
			}
			if len(d.Actions()) != 0 {
				t.Fatal("lease loser accessed workloads")
			}
		})
	}
}

func TestCreateFailureReleasesLeaseAndRetryRecovers(t *testing.T) {
	r, cm, d := testRunner(t)
	fail := true
	d.PrependReactor("create", "jobsets", func(clienttesting.Action) (bool, runtime.Object, error) {
		if fail {
			return true, nil, apierrors.NewServiceUnavailable("retryable outage")
		}
		return false, nil, nil
	})
	if err := r.tick(context.Background()); err == nil {
		t.Fatal("expected source failure")
	}
	state, _ := r.kube.CoreV1().ConfigMaps(cm.Namespace).Get(context.Background(), cm.Name, metav1.GetOptions{})
	if state.Annotations[leaseHolder] != "" || state.Data["last-error"] == "" {
		t.Fatal("failure did not release and record state")
	}
	fail = false
	if err := r.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestActiveAndRetainedRootBounds(t *testing.T) {
	for _, scenario := range []string{"active", "cap", "missing-time"} {
		t.Run(scenario, func(t *testing.T) {
			r, cm, d := testRunner(t)
			count := 1
			if scenario == "cap" {
				count = maxRoots
			}
			for i := range count {
				finish := r.now()
				if scenario == "active" {
					finish = time.Time{}
				}
				obj := addRoot(t, r, d, cm, workloadTypes[0], int64(i), finish)
				if scenario == "missing-time" {
					obj.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Succeeded", "status": "True"}}}
					d.Tracker().Update(workloadTypes[0].resource, obj, cm.Namespace)
				}
			}
			result, err := r.reconcile(context.Background(), cm)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "missing-time" && !strings.HasPrefix(result, "skipped") {
				t.Fatalf("expected bounded skip: %s", result)
			}
			for _, a := range d.Actions() {
				if a.GetVerb() == "delete" || (scenario != "missing-time" && a.GetVerb() == "create") {
					t.Fatalf("unexpected %s", a.GetVerb())
				}
			}
		})
	}
}

func TestCleanupOwnershipStateAndUIDPreconditions(t *testing.T) {
	for _, scenario := range []string{"old", "new", "foreign", "replaced", "became-active", "invalid-owner"} {
		t.Run(scenario, func(t *testing.T) {
			r, cm, d := testRunner(t)
			finish := r.now().Add(-retention - time.Minute)
			if scenario == "new" {
				finish = r.now()
			}
			obj := addRoot(t, r, d, cm, workloadTypes[0], 1, finish)
			switch scenario {
			case "foreign":
				obj.SetLabels(map[string]string{ownerLabel: "another-owner"})
				d.Tracker().Update(workloadTypes[0].resource, obj, cm.Namespace)
			case "invalid-owner":
				obj.SetOwnerReferences(nil)
				d.Tracker().Update(workloadTypes[0].resource, obj, cm.Namespace)
			case "replaced", "became-active":
				d.PrependReactor("get", "jobsets", func(clienttesting.Action) (bool, runtime.Object, error) {
					changed := obj.DeepCopy()
					if scenario == "replaced" {
						changed.SetUID("replacement-uid")
					} else {
						delete(changed.Object, "status")
					}
					return true, changed, nil
				})
			}
			_, err := r.reconcile(context.Background(), cm)
			wantErr := scenario == "replaced" || scenario == "became-active" || scenario == "invalid-owner"
			if (err != nil) != wantErr {
				t.Fatalf("error=%v wantError=%v", err, wantErr)
			}
			deletes := 0
			for _, action := range d.Actions() {
				if action.GetVerb() == "delete" {
					deletes++
					options := action.(clienttesting.DeleteAction).GetDeleteOptions()
					if options.Preconditions == nil || *options.Preconditions.UID != obj.GetUID() || *options.Preconditions.ResourceVersion != "7" {
						t.Fatalf("missing current UID/version precondition: %+v", options)
					}
				}
			}
			expected := 0
			if scenario == "old" {
				expected = 1
			}
			if deletes != expected {
				t.Fatalf("delete count=%d want=%d", deletes, expected)
			}
		})
	}
}

func TestFixturesAreFiniteAndRetainCompletedPods(t *testing.T) {
	r, cm, _ := testRunner(t)
	for _, w := range workloadTypes {
		t.Run(w.kind, func(t *testing.T) {
			obj := r.fixture(w, 100, cm)
			data, err := json.Marshal(obj.Object)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			for _, required := range []string{"activeDeadlineSeconds\":300", "sleep 1", "-lt 90", "finite CPU fixture completed", "restartPolicy\":\"Never", "automountServiceAccountToken\":false"} {
				if !strings.Contains(text, required) {
					t.Fatalf("missing %s", required)
				}
			}
			if strings.Contains(text, "nvidia.com/gpu") || strings.Contains(text, "ttlSecondsAfterFinished") {
				t.Fatal("fixture must be CPU only and retain evidence until explicit cleanup")
			}
			if strings.Count(text, "\"cpu\":\"50m\"") != 2 {
				t.Fatal("expected two bounded Pods")
			}
			if w.kind == "PyTorchJob" && !strings.Contains(text, "cleanPodPolicy\":\"None") {
				t.Fatal("completed PyTorch Pods would be removed")
			}
		})
	}
}
