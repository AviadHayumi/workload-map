// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metadatafake "k8s.io/client-go/metadata/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestIdentityMetadataUpdateAndPodReplacement(t *testing.T) {
	c, client, job := fixture(t)
	e, err := identity.New(identity.Config{Rules: []identity.Rule{{Name: "test", MatchExpression: "true", SubmittedByExpression: "'user' in annotations ? annotations['user'] : ''", IdentitySourceExpression: "'tenant'", SubmitterTypeExpression: "'unknown'"}}})
	if err != nil {
		t.Fatal(err)
	}
	c.options.Identity = e
	job.SetAnnotations(map[string]string{"user": "alice"})
	putJob(t, c, client, job)
	if got := c.store.Snapshot().Identities[job.GetUID()].Result.Submitter; got != "alice" {
		t.Fatalf("root: %q", got)
	}
	job = job.DeepCopy()
	job.SetAnnotations(map[string]string{"user": "bob"})
	putJob(t, c, client, job)
	if got := c.store.Snapshot().Identities[job.GetUID()].Result.Submitter; got != "bob" {
		t.Fatalf("same-generation metadata ignored: %q", got)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "old", Annotations: map[string]string{"user": "alice"}, OwnerReferences: jobOwner("missing", "missing")}}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	c.attributePod(pod)
	if got := c.store.Snapshot().Identities[pod.UID].Result.Submitter; got != "alice" {
		t.Fatalf("unresolved owner lost identity: %q", got)
	}
	replacement := pod.DeepCopy()
	replacement.UID = "new"
	replacement.Annotations = nil
	if err := c.podInformer.GetStore().Update(replacement); err != nil {
		t.Fatal(err)
	}
	c.onPodUpdate(pod, replacement)
	c.attributePod(pod)
	snapshot := c.store.Snapshot()
	if _, exists := snapshot.Identities[pod.UID]; exists {
		t.Fatal("old lifetime resurrected")
	}
	if snapshot.Identities[replacement.UID].Result.Status != identity.Unknown {
		t.Fatal("new lifetime inherited identity")
	}
	if err := c.podInformer.GetStore().Delete(replacement); err != nil {
		t.Fatal(err)
	}
	c.onPodDelete(replacement)
	if _, exists := c.store.Snapshot().Identities[replacement.UID]; exists {
		t.Fatal("deletion retained identity")
	}
}

func TestIdentityVerificationRejectsPendingMetadataEvaluation(t *testing.T) {
	c, client, job := fixture(t)
	e, err := identity.New(identity.Config{Rules: []identity.Rule{{Name: "test", MatchExpression: "true", SubmittedByExpression: "'user' in annotations ? annotations['user'] : ''", IdentitySourceExpression: "'tenant'", SubmitterTypeExpression: "'unknown'"}}})
	if err != nil {
		t.Fatal(err)
	}
	c.options.Identity = e
	job.SetAnnotations(map[string]string{"user": "alice"})
	putJob(t, c, client, job)
	c.verifyHealth(t.Context())
	if workloadHealth(t, c) != 1 {
		t.Fatal("fresh identity should be healthy")
	}
	changed := job.DeepCopy()
	changed.SetAnnotations(map[string]string{"user": "bob"})
	jobs := client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace("test")
	if _, err := jobs.Update(t.Context(), changed, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	// The informer has the new object, but its event has not been evaluated yet.
	if err := c.rootWatcherStore(job.GroupVersionKind().GroupKind()).Update(changed); err != nil {
		t.Fatal(err)
	}
	c.verifyHealth(t.Context())
	if workloadHealth(t, c) != 0 {
		t.Fatal("fresh informer metadata hid an old identity result")
	}
	c.processWorkload(changed, false)
	c.verifyHealth(t.Context())
	if workloadHealth(t, c) != 1 || c.store.Snapshot().Identities[job.GetUID()].Result.Submitter != "bob" {
		t.Fatal("processed metadata did not recover identity health")
	}
}

func TestMetadataOnlyIdentityDoesNotInventWorkload(t *testing.T) {
	for _, namespace := range []string{"test", ""} {
		t.Run("namespace="+namespace, func(t *testing.T) {
			c, _, job := fixture(t)
			e, err := identity.New(identity.Config{Rules: []identity.Rule{{Name: "test", MatchExpression: "true", SubmittedByExpression: "annotations['user']", IdentitySourceExpression: "'tenant'", SubmitterTypeExpression: "'unknown'"}}})
			if err != nil {
				t.Fatal(err)
			}
			c.options.Identity = e
			c.registry = registry.New()
			c.store.DeleteWorkload(job.GetUID())
			gvk := job.GroupVersionKind()
			c.options.IdentityKinds = []schema.GroupVersionKind{gvk}
			watcher := c.watchers[gvk]
			watcher.isRoot = false
			obj := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind}, ObjectMeta: metav1.ObjectMeta{Name: job.GetName(), Namespace: namespace, UID: job.GetUID()}}
			obj.SetAnnotations(map[string]string{"user": "alice"})
			if err := watcher.informer.GetStore().Update(obj); err != nil {
				t.Fatal(err)
			}
			c.onChildEvent(obj, gvk)
			snapshot := c.store.Snapshot()
			if snapshot.Identities[obj.UID].Result.Submitter != "alice" || len(snapshot.Workloads) != 0 {
				t.Fatal("metadata observation must retain identity without inventing a workload")
			}
			c.onChildDelete(obj, gvk)
			if _, ok := c.store.Snapshot().Identities[obj.UID]; ok {
				t.Fatal("metadata-only deletion retained identity")
			}
		})
	}
}

func TestMetadataVerificationUsesInformerRepresentation(t *testing.T) {
	c, client, job := fixture(t)
	e, err := identity.New(identity.Config{Rules: []identity.Rule{{Name: "test", MatchExpression: "true", SubmittedByExpression: "'user' in annotations ? annotations['user'] : ''", IdentitySourceExpression: "'tenant'", SubmitterTypeExpression: "'unknown'"}}})
	if err != nil {
		t.Fatal(err)
	}
	c.options.Identity = e
	c.registry = registry.New()
	c.store.DeleteWorkload(job.GetUID())
	gvk := job.GroupVersionKind()
	c.options.IdentityKinds = []schema.GroupVersionKind{gvk}
	c.watchers[gvk].isRoot = false
	job.SetAnnotations(map[string]string{"conversion.example.test/annotation": "converted"})
	if _, err := client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace("test").Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	fresh := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: job.GetName(), Namespace: job.GetNamespace(), UID: job.GetUID(), ResourceVersion: job.GetResourceVersion()}}
	c.metadataClient.(*metadatafake.FakeMetadataClient).PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, &metav1.List{Items: []runtime.RawExtension{{Object: fresh.DeepCopy()}}}, nil
	})
	if err := c.watchers[gvk].informer.GetStore().Update(fresh.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	c.onChildEvent(fresh, gvk)
	c.verifyHealth(t.Context())
	healthy := func() bool {
		for _, h := range c.store.Snapshot().Health {
			if h.Scope == "resource_type" && h.Ref.Kind == gvk.Kind {
				return h.Healthy
			}
		}
		return false
	}
	if !healthy() {
		t.Fatal("identical metadata must be healthy even when the full API view adds a conversion annotation")
	}
	fresh.SetAnnotations(map[string]string{"user": "bob"})
	fresh.SetResourceVersion("2")
	c.verifyHealth(t.Context())
	if healthy() {
		t.Fatal("metadata comparison hid a real unseen update")
	}
	if err := c.watchers[gvk].informer.GetStore().Update(fresh.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	c.onChildEvent(fresh, gvk)
	c.verifyHealth(t.Context())
	if !healthy() || c.store.Snapshot().Identities[fresh.UID].Result.Submitter != "bob" {
		t.Fatal("fresh metadata failed to recover health and identity")
	}
}
