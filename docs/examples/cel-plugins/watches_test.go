// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestParentPredicateTracksInputsAndOwnership(t *testing.T) {
	predicate := parentPredicate()
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
		want   bool
	}{
		{name: "status only", mutate: func(o *unstructured.Unstructured) { o.Object["status"] = map[string]any{"readyReplicas": int64(1)} }},
		{name: "result only", mutate: func(o *unstructured.Unstructured) {
			annotations := o.GetAnnotations()
			annotations[annotationResult] = `{"state":"Applied"}`
			o.SetAnnotations(annotations)
		}},
		{name: "spec", mutate: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(1), "spec", "replicas")
		}, want: true},
		{name: "state", mutate: func(o *unstructured.Unstructured) {
			annotations := o.GetAnnotations()
			annotations[annotationState] = `{}`
			o.SetAnnotations(annotations)
		}, want: true},
		{name: "last input removal", mutate: func(o *unstructured.Unstructured) { o.SetAnnotations(map[string]string{}) }, want: true},
		{name: "metadata label", mutate: func(o *unstructured.Unstructured) { o.SetLabels(map[string]string{"user": "changed"}) }, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := testWorkload()
			old.SetAnnotations(map[string]string{annotationPool: "general"})
			next := old.DeepCopy()
			tc.mutate(next)
			if got := predicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: next}); got != tc.want {
				t.Fatalf("Update() = %v, want %v", got, tc.want)
			}
		})
	}
	if !predicate.Create(event.CreateEvent{Object: testWorkload()}) {
		t.Fatal("initial listing create event was filtered")
	}
}

func TestChildUpdatesMapOldAndNewParentIdentity(t *testing.T) {
	parent := testWorkload()
	old := &corev1.Service{ObjectMeta: childMetadata(parent, "child", true)}
	next := old.DeepCopy()
	next.Annotations[parentNameKey] = "renamed"
	gvk := parent.GroupVersionKind()
	mapper := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request { return childRequests(obj, gvk) })
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()
	mapper.Update(context.Background(), event.UpdateEvent{ObjectOld: old, ObjectNew: next}, queue)
	got := map[string]bool{}
	for queue.Len() > 0 {
		req, _ := queue.Get()
		got[req.Namespace+"/"+req.Name] = true
		queue.Done(req)
	}
	if !reflect.DeepEqual(got, map[string]bool{"test/web": true, "test/renamed": true}) {
		t.Fatalf("mapped requests = %v", got)
	}
	for _, mutate := range []func(*corev1.Service){
		func(s *corev1.Service) { delete(s.Labels, managedByKey) },
		func(s *corev1.Service) { delete(s.Labels, workloadUIDKey) },
		func(s *corev1.Service) { s.Annotations[parentKindKey] = "CronJob" },
		func(s *corev1.Service) { s.Annotations[parentAPIVersionKey] = "apps/v1beta1" },
		func(s *corev1.Service) { s.Annotations[parentNameKey] = "invalid/name" },
	} {
		invalid := old.DeepCopy()
		mutate(invalid)
		if got := childRequests(invalid, gvk); len(got) != 0 {
			t.Fatalf("invalid child identity mapped: %v", got)
		}
	}
}
