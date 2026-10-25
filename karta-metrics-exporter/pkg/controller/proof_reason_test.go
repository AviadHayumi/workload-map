// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	collector "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/adapters/otel"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

func collectProofMetrics(publications store.Capturer) func(context.Context) metricdata.ResourceMetrics {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithCardinalityLimit(0))
	registration, err := collector.New().Register(provider.Meter("proof-test"), publications)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(registration.Unregister()).To(Succeed())
		Expect(provider.Shutdown(context.Background())).To(Succeed())
	})
	return func(ctx context.Context) metricdata.ResourceMetrics {
		var data metricdata.ResourceMetrics
		Expect(reader.Collect(ctx, &data)).To(Succeed())
		return data
	}
}

func workloadReasons(data metricdata.ResourceMetrics) []string {
	var reasons []string
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != collector.MetricExporterObservationValid {
				continue
			}
			for _, point := range metric.Data.(metricdata.Gauge[float64]).DataPoints {
				scope, _ := point.Attributes.Value(attribute.Key("scope"))
				if scope.AsString() != "workload" {
					continue
				}
				reason, _ := point.Attributes.Value(attribute.Key("reason"))
				Expect(point.Value).To(BeZero())
				reasons = append(reasons, reason.AsString())
			}
		}
	}
	return reasons
}

var _ = Describe("Proof retirement reasons", func() {
	DescribeTable("reports superseded status verification without inventing a missing owner", func(ctx SpecContext, unrelatedOwner bool) {
		r, _, publications := testRuntime(nil)
		r.deps.Inventory = jobInventory()
		root := testObject(testRootGVK, "root")
		root.SetCreationTimestamp(metav1.NewTime(time.Unix(100, 0)))
		root.Object["spec"] = map[string]any{"parallelism": int64(1)}
		root.Object["status"] = map[string]any{"active": int64(1)}
		w := seedWatch(r, testRootGVK, "jobs", root)
		seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		collect := collectProofMetrics(publications)
		before := publications.Published(root.GetUID())
		Expect(before.Workload.HasStatus).To(BeTrue())
		changed := root.DeepCopy()
		changed.SetResourceVersion("2")
		changed.Object["status"] = map[string]any{"failed": int64(1)}
		if unrelatedOwner {
			changed.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "unrelated", UID: "unrelated", Controller: ptr.To(false)}})
		}
		r.acceptEvent(w, changed, false)
		view := publications.Capture()
		for _, proof := range []store.Evidence{before.Creation, before.Status} {
			valid, reason := view.Valid(proof)
			Expect(valid).To(BeFalse())
			Expect(reason).To(Equal(store.ReasonAPIUnavailable))
		}
		metrics := collect(ctx)
		Expect(workloadReasons(metrics)).To(Equal([]string{store.ReasonAPIUnavailable}))
		phases := 0
		for _, scope := range metrics.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if metric.Name != collector.MetricWorkloadStatus {
					continue
				}
				for _, point := range metric.Data.(metricdata.Gauge[float64]).DataPoints {
					valid, _ := point.Attributes.Value(attribute.Key("observation_valid"))
					owner, _ := point.Attributes.Value(attribute.Key("owner_resolution"))
					Expect(valid.AsString()).To(Equal("false"))
					Expect(owner.AsString()).To(Equal("resolved"))
					phases++
				}
			}
		}
		Expect(phases).To(Equal(9))
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(workloadReasons(collect(ctx))).To(BeEmpty())
	}, Entry("status-only RV update", false), Entry("non-controller owner reference added", true))

	DescribeTable("classifies metadata verification changes before reconciliation", func(ctx SpecContext, change, expected string) {
		r, metadata, publications := testRuntime(nil)
		root, middle, pod := testObject(testRootGVK, "root"), testObject(testRootGVK, "middle"), testObject(podGVK, "pod")
		ownedBy(middle, root)
		ownedBy(pod, middle)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		key := rememberOwner(r.reader, "middle", "1")
		old := r.reader.metadata[key]
		old.object.SetOwnerReferences(middle.GetOwnerReferences())
		Expect(r.resolve(ctx, queueKey{Operation: "object", Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		row, _ := publications.Published(root.GetUID()).Pod(pod.GetUID())
		changed := old.object.DeepCopy()
		changed.ResourceVersion = "2"
		if change == "replacement" {
			changed.UID = "new-middle"
		}
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			if change == "missing" {
				return true, metadataList(), nil
			}
			return true, metadataList(changed), nil
		})
		Expect(r.reader.refreshMetadataGroup(ctx, watchKey{Resource: testRootGVK.GroupVersion().WithResource("jobs"), Namespace: "ns"}, map[objectKey]metadataEntry{key: old})).To(Succeed())
		valid, reason := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(expected))
		Expect(workloadReasons(collectProofMetrics(publications)(ctx))).To(Equal([]string{expected}))
	}, Entry("same-owner RV update", "rv", store.ReasonAPIUnavailable), Entry("UID replacement", "replacement", store.ReasonUnresolvedOwner), Entry("verified absence", "missing", store.ReasonUnresolvedOwner))
	DescribeTable("keeps intermediate failure reasons specific to the observed change", func(ctx SpecContext, change, expected string) {
		r, _, publications := testRuntime(nil)
		root, middle, pod := testObject(testRootGVK, "root"), testObject(testRootGVK, "middle"), testObject(podGVK, "pod")
		ownedBy(middle, root)
		ownedBy(pod, middle)
		w := seedWatch(r, testRootGVK, "jobs", root, middle)
		seedWatch(r, podGVK, "pods", pod)
		Expect(r.resolve(ctx, queueKey{Operation: "object", Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		collect := collectProofMetrics(publications)
		row, found := publications.Published(root.GetUID()).Pod(pod.GetUID())
		Expect(found).To(BeTrue())
		changed := middle.DeepCopy()
		changed.SetResourceVersion("2")
		switch change {
		case "missing":
			r.acceptEvent(w, middle, true)
		case "replacement":
			changed.SetUID("new-middle")
			r.acceptEvent(w, changed, false)
		case "controller":
			ownedBy(changed, testObject(testRootGVK, "new-parent"))
			r.acceptEvent(w, changed, false)
		case "non-controller":
			changed.SetOwnerReferences(append(changed.GetOwnerReferences(), metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "extra", UID: "extra"}))
			r.acceptEvent(w, changed, false)
		case "malformed":
			owners := changed.GetOwnerReferences()
			owners[0].UID = ""
			changed.SetOwnerReferences(owners)
			r.acceptEvent(w, changed, false)
		case "multiple":
			owners := changed.GetOwnerReferences()
			owners = append(owners, owners[0])
			changed.SetOwnerReferences(owners)
			r.acceptEvent(w, changed, false)
		case "block-deletion":
			owners := changed.GetOwnerReferences()
			owners[0].BlockOwnerDeletion = ptr.To(true)
			changed.SetOwnerReferences(owners)
			r.acceptEvent(w, changed, false)
		default:
			r.acceptEvent(w, changed, false)
		}
		valid, reason := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(expected))
		Expect(workloadReasons(collect(ctx))).To(Equal([]string{expected}))
	}, Entry("ordinary RV", "rv", store.ReasonAPIUnavailable), Entry("non-controller addition", "non-controller", store.ReasonAPIUnavailable), Entry("blockOwnerDeletion change", "block-deletion", store.ReasonAPIUnavailable), Entry("deleted required owner", "missing", store.ReasonUnresolvedOwner), Entry("required owner UID replacement", "replacement", store.ReasonUnresolvedOwner), Entry("controller changed", "controller", store.ReasonUnresolvedOwner), Entry("malformed controller", "malformed", store.ReasonUnresolvedOwner), Entry("multiple controllers", "multiple", store.ReasonUnresolvedOwner))
})
