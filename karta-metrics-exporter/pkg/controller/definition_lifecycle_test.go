// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

func clusterDefinitions(r *Runtime, catalogEnabled bool) {
	r.config.Definitions = inventory.Config{Cluster: inventory.ClusterConfig{Enabled: true}, Catalog: inventory.CatalogConfig{Enabled: catalogEnabled}}
	var err error
	r.deps.Inventory, err = inventory.New(r.config.Definitions)
	Expect(err).NotTo(HaveOccurred())
	r.definitionsDirty = true
	Expect(r.refreshDefinitions()).To(MatchError(discovery.ErrPending))
}

func kartaObject(name, version string) *unstructured.Unstructured {
	definition, err := catalog.Get(testRootGVK)
	Expect(err).NotTo(HaveOccurred())
	definition.Name = name
	definition.UID = types.UID(name)
	definition.ResourceVersion = version
	definition.CreationTimestamp = metav1.NewTime(time.Unix(1, 0))
	data, err := kruntime.DefaultUnstructuredConverter.ToUnstructured(definition)
	Expect(err).NotTo(HaveOccurred())
	object := &unstructured.Unstructured{Object: data}
	object.SetGroupVersionKind(kartaGVK)
	return object
}

func kartaMetadata(object *unstructured.Unstructured) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: kartaGVK.GroupVersion().String(), Kind: kartaGVK.Kind}, ObjectMeta: metav1.ObjectMeta{Name: object.GetName(), UID: object.GetUID(), ResourceVersion: object.GetResourceVersion()}}
}

var _ = Describe("Runtime definition ownership", func() {
	DescribeTable("keeps publication proof and Pod caches when only Karta metadata changes", func(ctx SpecContext, statusOnly bool) {
		labels := &countedLabels{}
		r, _, publications := testRuntime(labels)
		clusterDefinitions(r, true)
		definition := kartaObject("job", "1")
		w := seedWatch(r, kartaGVK, "kartas", definition)
		root, first, second := testObject(testRootGVK, "root"), testObject(podGVK, "first"), testObject(podGVK, "second")
		ownedBy(first, root)
		ownedBy(second, root)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", first, second)
		state := seedRoot(r, root, first, second)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		before := publications.Published(root.GetUID())
		generation, key := state.generation, r.definitionKeys[testRootGVK.GroupKind()]
		firstCache, secondCache := state.pods[first.GetUID()].cached, state.pods[second.GetUID()].cached
		calls := labels.calls
		updated := definition.DeepCopy()
		updated.SetResourceVersion("2")
		if statusOnly {
			updated.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Validated", "message": "definition accepted", "lastTransitionTime": "2026-10-27T00:00:00Z"}}}
		}
		r.acceptEvent(w, updated, false)
		Expect(state.generation).To(Equal(generation))
		Expect(state.fullRefresh).To(BeFalse())
		Expect(state.dirty).To(BeEmpty())
		Expect(r.definitionKeys[testRootGVK.GroupKind()]).To(Equal(key))
		Expect(publications.Published(root.GetUID())).To(BeIdenticalTo(before))
		row, _ := before.Pod(first.GetUID())
		for _, proof := range []store.Evidence{before.Status, row.Mapping, before.Creation} {
			valid, _ := publications.Capture().Valid(proof)
			Expect(valid).To(BeTrue())
		}
		selection, err := r.deps.Inventory.Lookup(testRootGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.ResourceVersion).To(Equal("2"))
		Expect(r.selections[testRootGVK.GroupKind()].Origin.ResourceVersion).To(Equal("2"))
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(labels.calls).To(Equal(calls+1), "only workload status labels are evaluated on the clean root pass")
		Expect(state.pods[first.GetUID()].cached).To(BeIdenticalTo(firstCache))
		Expect(state.pods[second.GetUID()].cached).To(BeIdenticalTo(secondCache))
		Expect(publications.Published(root.GetUID()).PodPages[0]).To(BeIdenticalTo(before.PodPages[0]))
	}, Entry("resource version only", false), Entry("operator status update", true))

	DescribeTable("invalidates publication proof when the selected Karta meaning changes", func(ctx SpecContext, change string) {
		r, _, publications := testRuntime(nil)
		clusterDefinitions(r, true)
		definition := kartaObject("job", "1")
		w := seedWatch(r, kartaGVK, "kartas", definition)
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		state := seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		before := publications.Published(root.GetUID())
		generation, key := state.generation, r.definitionKeys[testRootGVK.GroupKind()]
		updated := definition.DeepCopy()
		updated.SetResourceVersion("2")
		switch change {
		case "spec":
			Expect(unstructured.SetNestedField(updated.Object, "", "spec", "structureDefinition", "rootComponent", "name")).To(Succeed())
		case "uid":
			updated.SetUID("replacement")
		case "gvk":
			Expect(unstructured.SetNestedField(updated.Object, "v2", "spec", "structureDefinition", "rootComponent", "kind", "version")).To(Succeed())
		}
		r.acceptEvent(w, updated, false)
		Expect(state.generation).To(BeNumerically(">", generation))
		Expect(r.definitionKeys[testRootGVK.GroupKind()]).NotTo(Equal(key))
		row, _ := before.Pod(pod.GetUID())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
		selection, err := r.lookupDefinition(testRootGVK)
		Expect(selection.Origin.Source).To(Equal(inventory.SourceCluster))
		if change == "spec" {
			Expect(errors.Is(err, inventory.ErrInvalidDefinition)).To(BeTrue())
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
	}, Entry("invalid winning spec", "spec"), Entry("recreated definition", "uid"), Entry("selected API representation", "gvk"))

	It("ignores an invalid losing candidate but invalidates when label filtering makes it win", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		clusterDefinitions(r, true)
		r.config.Definitions.Cluster.LabelSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"enabled": "yes"}}
		var err error
		r.deps.Inventory, err = inventory.New(r.config.Definitions)
		Expect(err).NotTo(HaveOccurred())
		first, second := kartaObject("first", "1"), kartaObject("second", "1")
		first.SetLabels(map[string]string{"enabled": "yes"})
		second.SetLabels(map[string]string{"enabled": "yes"})
		second.SetCreationTimestamp(metav1.NewTime(time.Unix(2, 0)))
		w := seedWatch(r, kartaGVK, "kartas", first, second)
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		state := seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		generation, key := state.generation, r.definitionKeys[testRootGVK.GroupKind()]
		before := publications.Published(root.GetUID())
		row, _ := before.Pod(pod.GetUID())
		second = second.DeepCopy()
		second.SetResourceVersion("2")
		Expect(unstructured.SetNestedField(second.Object, "", "spec", "structureDefinition", "rootComponent", "name")).To(Succeed())
		r.acceptEvent(w, second, false)
		Expect(state.generation).To(Equal(generation))
		Expect(r.definitionKeys[testRootGVK.GroupKind()]).To(Equal(key))
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeTrue())
		first = first.DeepCopy()
		first.SetResourceVersion("2")
		first.SetLabels(map[string]string{"enabled": "no"})
		r.acceptEvent(w, first, false)
		Expect(state.generation).To(BeNumerically(">", generation))
		selection, err := r.lookupDefinition(testRootGVK)
		Expect(errors.Is(err, inventory.ErrInvalidDefinition)).To(BeTrue())
		Expect(selection.Origin.Name).To(Equal("second"))
		valid, _ = publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
	})

	It("rejects an in-flight build across a Karta status update and retries its work", func(ctx SpecContext) {
		paused := &pausedLabels{entered: make(chan types.UID, 1), resume: make(chan struct{})}
		r, _, publications := testRuntime(paused)
		clusterDefinitions(r, true)
		definition := kartaObject("job", "1")
		w := seedWatch(r, kartaGVK, "kartas", definition)
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		state := seedRoot(r, root, pod)
		generation, key := state.generation, r.definitionProofLocked(testRootGVK.GroupKind())
		done := make(chan error, 1)
		go func() { done <- r.reconcile(ctx, root.GetUID()) }()
		Eventually(paused.entered).Should(Receive())
		updated := definition.DeepCopy()
		updated.SetResourceVersion("2")
		r.acceptEvent(w, updated, false)
		Expect(state.generation).To(Equal(generation))
		Expect(r.definitionKeys[testRootGVK.GroupKind()]).To(Equal(key))
		close(paused.resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(publications.Published(root.GetUID())).To(BeNil())
		Expect(state.fullRefresh).To(BeTrue())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(state.publishedOrigin.ResourceVersion).To(Equal("2"))
		row, found := publications.Published(root.GetUID()).Pod(pod.GetUID())
		Expect(found).To(BeTrue())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeTrue())
	})

	It("blocks initial catalog fallback while retaining root discovery and independent creation proof", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		clusterDefinitions(r, true)
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(r.roots).To(HaveKey(root.GetUID()))
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		record := publications.Published(root.GetUID())
		Expect(record.Workload.HasStatus).To(BeFalse())
		Expect(record.Workload.Karta).To(BeEmpty())
		valid, _ := publications.Capture().Valid(record.Creation)
		Expect(valid).To(BeTrue())
		row, found := record.Pod(pod.GetUID())
		Expect(found).To(BeTrue())
		valid, _ = publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
		w := seedWatch(r, kartaGVK, "kartas")
		w.ready = false
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{w.key.Resource: "KartaList"})
		r.reader.clients.Dynamic = client
		client.PrependReactor("list", "kartas", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(w.key.Resource.GroupResource(), "", errors.New("denied"))
		})
		_, err := r.listWatch(ctx, w, metav1.ListOptions{})
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
		selection, err := r.lookupDefinition(testRootGVK)
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
		Expect(selection.Definition).To(BeNil())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(publications.Published(root.GetUID()).Workload.Karta).To(BeEmpty())
		valid, _ = publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeTrue())
	})

	It("invalidates generic rows synchronously when a Karta is accepted", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		clusterDefinitions(r, false)
		w := seedWatch(r, kartaGVK, "kartas")
		Expect(r.refreshDefinitions()).To(Succeed())
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		before := publications.Published(root.GetUID())
		row, _ := before.Pod(pod.GetUID())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeTrue())
		r.acceptEvent(w, kartaObject("job", "1"), false)
		valid, _ = publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse(), "accept must withdraw the previous generic interpretation before returning")
		valid, _ = publications.Capture().Valid(before.Creation)
		Expect(valid).To(BeTrue())
		selection, err := r.lookupDefinition(testRootGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.Name).To(Equal("job"))
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(publications.Published(root.GetUID()).Workload.Karta).To(Equal("job"))
	})

	It("keeps unrelated generic workload kinds valid when a Karta is accepted", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		clusterDefinitions(r, false)
		w := seedWatch(r, kartaGVK, "kartas")
		Expect(r.refreshDefinitions()).To(Succeed())
		deploymentGVK := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
		job, deployment := testObject(testRootGVK, "job"), testObject(deploymentGVK, "deployment")
		jobPod, deploymentPod := testObject(podGVK, "job-pod"), testObject(podGVK, "deployment-pod")
		ownedBy(jobPod, job)
		ownedBy(deploymentPod, deployment)
		seedWatch(r, testRootGVK, "jobs", job)
		seedWatch(r, deploymentGVK, "deployments", deployment)
		seedWatch(r, podGVK, "pods", jobPod, deploymentPod)
		seedRoot(r, job, jobPod)
		seedRoot(r, deployment, deploymentPod)
		Expect(r.reconcile(ctx, job.GetUID())).To(Succeed())
		Expect(r.reconcile(ctx, deployment.GetUID())).To(Succeed())
		jobRecord, deploymentRecord := publications.Published(job.GetUID()), publications.Published(deployment.GetUID())
		jobRow, _ := jobRecord.Pod(jobPod.GetUID())
		deploymentRow, _ := deploymentRecord.Pod(deploymentPod.GetUID())
		for _, proof := range []store.Evidence{jobRow.Mapping, deploymentRow.Mapping} {
			valid, _ := publications.Capture().Valid(proof)
			Expect(valid).To(BeTrue())
		}
		r.acceptEvent(w, kartaObject("job", "1"), false)
		valid, _ := publications.Capture().Valid(jobRow.Mapping)
		Expect(valid).To(BeFalse())
		for _, proof := range []store.Evidence{deploymentRow.Mapping, jobRecord.Creation, deploymentRecord.Creation} {
			valid, _ := publications.Capture().Valid(proof)
			Expect(valid).To(BeTrue())
		}
	})

	DescribeTable("expires Karta evidence at its exact deadline while keeping creation independent", func(ctx SpecContext, populated, catalogEnabled bool) {
		r, metadata, _ := testRuntime(nil)
		clock := renewalClock(r)
		publications := store.New(store.Options{Now: clock.Now})
		r.deps.Publications, r.reader.publications = publications, publications
		clusterDefinitions(r, catalogEnabled)
		definition := kartaObject("job", "1")
		var objects []*unstructured.Unstructured
		if populated {
			objects = append(objects, definition)
		}
		w := seedWatch(r, kartaGVK, "kartas", objects...)
		w.verifiedAt = clock.Now()
		Expect(r.refreshDefinitions()).To(Succeed())
		deadline := clock.Now().Add(r.config.MaxAge)
		clock.Step(r.config.MaxAge / 2)
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		before := publications.Published(root.GetUID())
		row, _ := before.Pod(pod.GetUID())
		Expect(before.Status.ExpiresAt).To(Equal(deadline))
		for _, proof := range []store.Evidence{before.Status, row.Mapping, before.Creation} {
			valid, _ := publications.Capture().Valid(proof)
			Expect(valid).To(BeTrue())
		}
		clock.Step(deadline.Sub(clock.Now()))
		for _, proof := range []store.Evidence{before.Status, row.Mapping} {
			valid, reason := publications.Capture().Valid(proof)
			Expect(valid).To(BeFalse())
			Expect(reason).To(Equal(store.ReasonAPIUnavailable))
		}
		valid, _ := publications.Capture().Valid(before.Creation)
		Expect(valid).To(BeTrue())
		Expect(r.refreshDefinitions()).To(MatchError(discovery.ErrUnavailable))
		_, err := r.lookupDefinition(testRootGVK)
		Expect(err).To(MatchError(discovery.ErrUnavailable))
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		invalid := publications.Published(root.GetUID())
		Expect(invalid.Workload.Karta).To(Equal(before.Workload.Karta))
		row, _ = invalid.Pod(pod.GetUID())
		valid, _ = publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse(), "source failure cannot authorize catalog or generic fallback")
		valid, _ = publications.Capture().Valid(invalid.Creation)
		Expect(valid).To(BeTrue())
		metadata.PrependReactor("list", "kartas", func(clienttesting.Action) (bool, kruntime.Object, error) {
			if populated {
				return true, metadataList(kartaMetadata(definition)), nil
			}
			return true, metadataList(), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		Expect(r.refreshDefinitions()).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		fresh := publications.Published(root.GetUID())
		row, _ = fresh.Pod(pod.GetUID())
		for _, proof := range []store.Evidence{fresh.Status, row.Mapping, fresh.Creation} {
			valid, _ := publications.Capture().Valid(proof)
			Expect(valid).To(BeTrue())
		}
		Expect(fresh.Workload.Karta).To(Equal(before.Workload.Karta))
	}, Entry("selected cluster definition", true, true), Entry("empty cluster with catalog selection", false, true), Entry("empty cluster with generic ownership", false, false))

	DescribeTable("retires recovered definition source keys without changing captured failures", func(referenced bool) {
		r, _, publications := testRuntime(nil)
		clusterDefinitions(r, false)
		w := seedWatch(r, kartaGVK, "kartas")
		Expect(r.refreshDefinitions()).To(Succeed())
		var retired []store.Evidence
		var captured []store.CapturedView
		for range 10 {
			proof := store.Evidence{ExpiresAt: time.Now().Add(time.Hour), Dependencies: []string{r.definitionKey}}
			if referenced {
				publications.Publish(&store.Publication{Workload: store.WorkloadRecord{UID: "root"}, Status: proof})
				publications.SetPublicationHealth([]store.HealthRecord{{GVK: kartaGVK, Evidence: proof}})
			}
			w.err = discovery.ErrUnavailable
			Expect(r.refreshDefinitions()).To(MatchError(discovery.ErrUnavailable))
			captured = append(captured, publications.Capture())
			retired = append(retired, proof)
			w.err = nil
			Expect(r.refreshDefinitions()).To(Succeed())
			Expect(r.definitionKey).NotTo(Equal(proof.Dependencies[0]))
			if referenced {
				valid, _ := publications.Capture().Valid(proof)
				Expect(valid).To(BeFalse(), "live publication and health references retain the failure")
				publications.RemovePublication("root")
				valid, _ = publications.Capture().Valid(proof)
				Expect(valid).To(BeFalse(), "health still refers to the failed generation")
				publications.SetPublicationHealth(nil)
			}
			for i, old := range retired {
				valid, _ := publications.Capture().Valid(old)
				Expect(valid).To(BeTrue(), "unreferenced failures must be released after recovery")
				valid, _ = captured[i].Valid(old)
				Expect(valid).To(BeFalse(), "captured failure state remains frozen")
			}
		}
	}, Entry("without publication references", false), Entry("with publication and health references", true))

	It("applies a LIST-confirmed deletion without waiting for a watch tombstone", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		clusterDefinitions(r, true)
		w := seedWatch(r, kartaGVK, "kartas", kartaObject("cluster-job", "1"))
		selection, err := r.lookupDefinition(testRootGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.Source).To(Equal(inventory.SourceCluster))
		metadata.PrependReactor("list", "kartas", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		selection, err = r.lookupDefinition(testRootGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.Source).To(Equal(inventory.SourceCatalog))
		Expect(w.objects).To(BeEmpty())
	})

	It("rejects stale UID tombstones and gates version mismatch until matching observation", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		clusterDefinitions(r, true)
		original := kartaObject("job", "1")
		w := seedWatch(r, kartaGVK, "kartas", original)
		replacement := original.DeepCopy()
		replacement.SetUID("replacement")
		replacement.SetResourceVersion("2")
		r.acceptEvent(w, replacement, false)
		r.acceptEvent(w, original, true)
		selection, err := r.lookupDefinition(testRootGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.UID).To(Equal(types.UID("replacement")))
		newVersion := replacement.DeepCopy()
		newVersion.SetResourceVersion("3")
		metadata.PrependReactor("list", "kartas", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(kartaMetadata(newVersion)), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		selection, err = r.lookupDefinition(testRootGVK)
		Expect(err).To(MatchError(discovery.ErrPending))
		Expect(selection.Origin.Source).To(Equal(inventory.SourceCluster))
		Expect(r.refreshDefinitions()).To(MatchError(discovery.ErrPending))
		r.acceptEvent(w, newVersion, false)
		selection, err = r.lookupDefinition(testRootGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.ResourceVersion).To(Equal("3"))
	})

	It("blocks fallback when a metadata LIST finds a definition before its watch event", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		clusterDefinitions(r, true)
		w := seedWatch(r, kartaGVK, "kartas")
		Expect(r.refreshDefinitions()).To(Succeed())
		definition := kartaObject("job", "1")
		metadata.PrependReactor("list", "kartas", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(kartaMetadata(definition)), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		_, err := r.lookupDefinition(testRootGVK)
		Expect(err).To(MatchError(discovery.ErrPending))
		r.acceptEvent(w, definition, false)
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		selection, err := r.lookupDefinition(testRootGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.Name).To(Equal("job"))
	})

	It("makes Karta EOF unavailable until a fresh verification recovers the accepted snapshot", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		clusterDefinitions(r, true)
		definition := kartaObject("job", "1")
		w := seedWatch(r, kartaGVK, "kartas", definition)
		root := testObject(testRootGVK, "root")
		seedWatch(r, testRootGVK, "jobs", root)
		seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		before := publications.Published(root.GetUID())
		child, cancel := context.WithCancel(ctx)
		defer cancel()
		source := watch.NewRaceFreeFake()
		stream := r.monitorSource(child, w, source, nil)
		defer stream.Stop()
		source.Stop()
		Eventually(func() error { _, err := r.lookupDefinition(testRootGVK); return err }).Should(MatchError("karta watch closed"))
		valid, reason := publications.Capture().Valid(before.Status)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(store.ReasonAPIUnavailable))
		valid, _ = publications.Capture().Valid(before.Creation)
		Expect(valid).To(BeTrue())
		Expect(r.selections).To(HaveKey(testRootGVK.GroupKind()))
		metadata.PrependReactor("list", "kartas", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(kartaMetadata(definition)), nil
		})
		Expect(r.verifyWatch(context.Background(), w.key)).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		valid, _ = publications.Capture().Valid(publications.Published(root.GetUID()).Status)
		Expect(valid).To(BeTrue())
	})
})
