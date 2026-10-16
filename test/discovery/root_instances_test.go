// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build live_discovery

package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

// JobSet instance names are immutable. A dedicated suspended fixture exercises
// warmed named-instance results, an allowed metadata update, and UID replacement.
// Same-UID declaration changes and actual cache hits are checked by unit tests.
func TestLiveJobSetNamedInstanceCache(t *testing.T) {
	if os.Getenv("KARTA_DISCOVERY_MUTATION_TESTS") != "true" {
		t.Skip("set KARTA_DISCOVERY_MUTATION_TESTS=true for isolated namespace mutation tests")
	}
	ctx, _, client, _, reader := liveClient(t)
	namespaces := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	namespace := fmt.Sprintf("sdk-instance-cache-%d", time.Now().UnixNano())
	if _, err := namespaces.Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := namespaces.Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete test namespace: %v", err)
		}
	})
	roots := client.Resource(schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}).Namespace(namespace)
	create := func(id string) *unstructured.Unstructured {
		object := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "jobset.x-k8s.io/v1alpha2", "kind": "JobSet", "metadata": map[string]any{"name": "named-instances"},
			"spec": map[string]any{"suspend": true, "replicatedJobs": []any{map[string]any{"name": id, "replicas": int64(1), "template": map[string]any{"spec": map[string]any{"parallelism": int64(1), "completions": int64(1), "template": map[string]any{"spec": map[string]any{"restartPolicy": "Never", "nodeSelector": map[string]any{"discovery.example.com/unschedulable": "true"}, "containers": []any{map[string]any{"name": "work", "image": "busybox:1.37"}}}}}}}}},
		}}
		created, err := roots.Create(ctx, object, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	root := create("alpha")
	dataset, err := sdk.Collect(ctx, reader, namespace, liveRef(root))
	if err != nil {
		t.Fatal(err)
	}
	index := sdk.NewIndex()
	generation, err := index.Replace(ctx, dataset)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := sdk.New(index, sdk.Options{Cache: &sdk.CacheOptions{MaxWorkloads: 4, MaxPods: 10}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := resolver.Workload(ctx, liveRef(root))
	if err != nil {
		t.Fatal(err)
	}
	assertLiveDeclaredInstance(t, first, "alpha", "gamma")
	for range 2 {
		warm, err := resolver.Workload(ctx, liveRef(root))
		if err != nil {
			t.Fatal(err)
		}
		assertLiveDeclaredInstance(t, warm, "alpha", "gamma")
		if warm == first || warm.Observation() == first.Observation() {
			t.Fatal("warm result reused observation handle or timestamps")
		}
	}
	forcedConflict, observedConflict := false, false
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := roots.Get(ctx, root.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !forcedConflict {
			// Publish a newer version so the first metadata update must retry.
			bumped := current.DeepCopy()
			annotations := bumped.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations["discovery.example.com/retry"] = "exercise-conflict"
			bumped.SetAnnotations(annotations)
			if _, err := roots.Update(ctx, bumped, metav1.UpdateOptions{}); err != nil {
				return err
			}
			forcedConflict = true
		}
		annotations := current.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["discovery.example.com/revision"] = "changed"
		current.SetAnnotations(annotations)
		updated, err := roots.Update(ctx, current, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			observedConflict = true
		}
		if err == nil {
			root = updated
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !observedConflict {
		t.Fatal("metadata update did not exercise the conflict retry")
	}
	generation, err = index.Apply(ctx, generation, sdk.Changes{Upserts: []*unstructured.Unstructured{root}})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := resolver.Workload(ctx, liveRef(root))
	if err != nil {
		t.Fatal(err)
	}
	assertLiveDeclaredInstance(t, changed, "alpha", "gamma")
	if changed.Object().GetAnnotations()["discovery.example.com/revision"] != "changed" || changed.Observation().Generation != generation {
		t.Fatal("allowed root update reused stale body or observation")
	}
	if first.Object().GetAnnotations()["discovery.example.com/revision"] != "" {
		t.Fatal("root update mutated prior observation")
	}
	old := liveRef(root)
	if err := roots.Delete(ctx, root.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr.To(root.GetUID())}}); err != nil {
		t.Fatal(err)
	}
	waitLive(t, ctx, func() (bool, error) {
		_, err := roots.Get(ctx, root.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	generation, err = index.Apply(ctx, generation, sdk.Changes{Deletes: []sdk.ObjectRef{old}})
	if err != nil {
		t.Fatal(err)
	}
	replacement := create("gamma")
	if replacement.GetUID() == old.UID {
		t.Fatal("replacement reused UID")
	}
	generation, err = index.Apply(ctx, generation, sdk.Changes{Upserts: []*unstructured.Unstructured{replacement}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Workload(ctx, old); !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatalf("retired root accepted: %v", err)
	}
	for range 2 {
		fresh, err := resolver.Workload(ctx, liveRef(replacement))
		if err != nil {
			t.Fatal(err)
		}
		assertLiveDeclaredInstance(t, fresh, "gamma", "alpha")
		if fresh.Root().UID != replacement.GetUID() || fresh.Observation().Generation != generation {
			t.Fatal("replacement did not supply current identity and observation")
		}
	}
	assertLiveDeclaredInstance(t, first, "alpha", "gamma")
	t.Log("suspended JobSet: repeated named scopes, allowed root update, and new-UID declarations passed; same-UID instance mutation is unit-tested")
}

func assertLiveDeclaredInstance(t *testing.T, workload *sdk.Workload, present, absent string) {
	t.Helper()
	if workload.Generic() || len(workload.MembershipIssues()) != 0 {
		t.Fatal("JobSet lost complete known attribution")
	}
	assertPods(t, workload.Pods(), nil)
	component, err := workload.Component("replicatedjob")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := component.Instance(present)
	if err != nil {
		t.Fatal(err)
	}
	pods, err := instance.Pods()
	if err != nil {
		t.Fatal(err)
	}
	assertPods(t, pods, nil)
	if _, err := component.Instance(absent); !errors.Is(err, sdk.ErrInstanceNotFound) {
		t.Fatalf("unexpected declared instance %q: %v", absent, err)
	}
}
