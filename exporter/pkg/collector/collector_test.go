// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

var fixtureTime = time.Date(2026, time.October, 20, 0, 0, 0, 0, time.UTC)

func fixtureStore() *store.Store {
	s := store.New()
	s.BindPublication(nil, func() time.Time { return fixtureTime })
	ref := store.WorkloadRef{Namespace: "team-a", Name: "llm", Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}
	publication := &store.Publication{
		Workload:    store.WorkloadRecord{UID: "w1", Ref: ref, Karta: "jobset-x-k8s-io-jobset-v1alpha2", KartaSource: "catalog", OwnerResolution: "resolved", HasStatus: true, CreatedTimestamp: 1792324800, Phases: []v1alpha1.ResourceStatus{v1alpha1.RunningStatus}},
		Status:      store.Evidence{ExpiresAt: fixtureTime.Add(time.Minute), Dependencies: []string{"status"}},
		Creation:    store.Evidence{ExpiresAt: fixtureTime.Add(time.Minute), Dependencies: []string{"root"}},
		Membership:  store.Evidence{ExpiresAt: fixtureTime.Add(time.Minute), Dependencies: []string{"membership"}},
		SubmittedBy: "alice",
	}
	for i, name := range []string{"prefill", "decode"} {
		uid := []string{"p1", "p2"}[i]
		publication = publication.WithPod("", &store.PublishedPod{
			Record:  store.PodRecord{UID: types.UID(uid), Namespace: "team-a", Name: name + "-0", WorkloadUID: "w1", Workload: ref, OwnerResolution: "resolved", Component: "replicatedjob", Instance: name},
			Mapping: store.Evidence{ExpiresAt: fixtureTime.Add(time.Minute), Dependencies: []string{"mapping:" + uid}},
		})
	}
	s.Publish(publication)
	s.SetPublicationHealth([]store.HealthRecord{
		{Scope: "resource_type", Ref: ref, Healthy: true, VerifiedAt: fixtureTime, MaxAge: time.Minute},
		{Scope: "workload", UID: "w1", Ref: ref, Healthy: true, VerifiedAt: fixtureTime, MaxAge: time.Minute},
	})
	return s
}

func TestExactKEPContract(t *testing.T) {
	metrics := gather(t, fixtureStore(), Options{})
	wantLabels := map[string][]string{
		"karta_pod_workload_info":                  {"namespace", "workload_name", "workload_group", "workload_version", "workload_kind", "workload_uid", "owner_resolution", "pod_name", "pod_uid", "component", "component_instance", "replica_group_id", "observation_valid"},
		"karta_workload_status":                    {"namespace", "workload_name", "workload_group", "workload_version", "workload_kind", "workload_uid", "phase", "owner_resolution", "karta", "karta_source", "observation_valid"},
		"karta_workload_created_timestamp_seconds": {"namespace", "workload_name", "workload_uid"},
		"karta_exporter_observation_valid":         {"scope", "namespace", "resource_group", "resource_version", "resource_kind", "workload_name", "workload_uid", "reason"},
	}
	if len(metrics) != len(wantLabels) {
		t.Fatalf("published %d metric families, want exactly %d: %v", len(metrics), len(wantLabels), metrics)
	}
	for name, labels := range wantLabels {
		slices.Sort(labels)
		if len(metrics[name]) == 0 {
			t.Fatalf("missing metric family %s", name)
		}
		for _, m := range metrics[name] {
			got := make([]string, 0, len(m.Label))
			for _, pair := range m.Label {
				got = append(got, pair.GetName())
			}
			slices.Sort(got)
			if !slices.Equal(got, labels) || m.Gauge == nil {
				t.Fatalf("%s labels=%v, want %v; gauge=%v", name, got, labels, m.Gauge)
			}
		}
	}
	if len(metrics[MetricPodWorkloadInfo]) != 2 || len(metrics[MetricWorkloadStatus]) != 9 || len(metrics[MetricWorkloadCreatedTimestamp]) != 1 || len(metrics[MetricExporterObservationValid]) != 1 {
		t.Fatal("unexpected snapshot cardinality")
	}
	for _, m := range metrics[MetricPodWorkloadInfo] {
		if label(m, LabelWorkloadUID) != "w1" || label(m, LabelPodUID) == "" || label(m, LabelWorkloadVersion) != "v1alpha2" || label(m, LabelOwnerResolution) != "resolved" || label(m, LabelObservationValid) != "true" || m.GetGauge().GetValue() != 1 {
			t.Fatalf("incorrect Pod identity: %v", m)
		}
	}
	for _, m := range metrics[MetricWorkloadStatus] {
		want := 0.0
		if label(m, LabelPhase) == "Running" {
			want = 1
		}
		if m.GetGauge().GetValue() != want || label(m, LabelKartaSource) != "catalog" || label(m, LabelKarta) != "jobset-x-k8s-io-jobset-v1alpha2" {
			t.Fatalf("incorrect dense phase or provenance: %v", m)
		}
	}
	if got := metrics[MetricWorkloadCreatedTimestamp][0].GetGauge().GetValue(); got != 1792324800 {
		t.Fatalf("creation = %v", got)
	}
	health := metrics[MetricExporterObservationValid][0]
	if label(health, "scope") != "resource_type" || label(health, LabelWorkload) != "" || label(health, LabelWorkloadUID) != "" || label(health, LabelReason) != "none" || health.GetGauge().GetValue() != 1 {
		t.Fatalf("resource health = %v", health)
	}
}

func TestIndependentValidity(t *testing.T) {
	for _, tc := range []struct {
		name, failure                                     string
		status, firstPod, secondPod, creation, membership bool
		workloadFailures                                  int
	}{
		{name: "healthy", status: true, firstPod: true, secondPod: true, creation: true, membership: true},
		{name: "status", failure: "status", firstPod: true, secondPod: true, creation: true, membership: true, workloadFailures: 1},
		{name: "mapping", failure: "mapping:p1", status: true, secondPod: true, creation: true, membership: true, workloadFailures: 1},
		{name: "creation", failure: "root", status: true, firstPod: true, secondPod: true, membership: true},
		{name: "membership", failure: "membership", status: true, firstPod: true, secondPod: true, creation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixtureStore()
			if tc.failure != "" {
				s.SetPublicationFailure(tc.failure, store.ReasonEvaluationFailed)
			}
			metrics := gather(t, s, Options{Identity: true})
			for _, row := range metrics[MetricWorkloadStatus] {
				if (label(row, LabelObservationValid) == "true") != tc.status || (label(row, LabelSubmittedBy) == "alice") != (tc.status && tc.membership) {
					t.Fatalf("status = %v", row)
				}
			}
			for _, row := range metrics[MetricPodWorkloadInfo] {
				valid := tc.firstPod
				if label(row, LabelPodUID) == "p2" {
					valid = tc.secondPod
				}
				if (label(row, LabelObservationValid) == "true") != valid || (label(row, LabelSubmittedBy) == "alice") != (valid && tc.membership) {
					t.Fatalf("mapping = %v", row)
				}
			}
			if (len(metrics[MetricWorkloadCreatedTimestamp]) == 1) != tc.creation {
				t.Fatal("creation validity was coupled to another field")
			}
			if got := len(metrics[MetricExporterObservationValid]); got != 1+tc.workloadFailures {
				t.Fatalf("health rows = %d, want %d", got, 1+tc.workloadFailures)
			}
		})
	}
}

func TestExpiryDoesNotRenewOnCollection(t *testing.T) {
	s := fixtureStore()
	s.BindPublication(nil, func() time.Time { return fixtureTime.Add(time.Minute) })
	for range 2 {
		metrics := gather(t, s, Options{Identity: true})
		if len(metrics[MetricWorkloadCreatedTimestamp]) != 0 {
			t.Fatal("expired creation time retained")
		}
		for _, name := range []string{MetricPodWorkloadInfo, MetricWorkloadStatus} {
			for _, row := range metrics[name] {
				if label(row, LabelObservationValid) != "false" || label(row, LabelSubmittedBy) != "" {
					t.Fatalf("expired row = %v", row)
				}
			}
		}
		if len(metrics[MetricExporterObservationValid]) != 2 {
			t.Fatal("missing expired workload/resource health")
		}
	}
}

func TestInitialFailureAndReasonPriority(t *testing.T) {
	s := fixtureStore()
	p := *s.Published("w1")
	p.Workload.HasStatus = false
	p.PodPages = nil
	p.FailureReasons = []string{store.ReasonEvaluationFailed, store.ReasonPermissionDenied, store.ReasonInvalidDefinition}
	s.Publish(&p)
	metrics := gather(t, s, Options{})
	if len(metrics[MetricPodWorkloadInfo]) != 0 || len(metrics[MetricWorkloadStatus]) != 0 || len(metrics[MetricWorkloadCreatedTimestamp]) != 1 {
		t.Fatal("initial failure invented rows or removed independent creation")
	}
	for _, row := range metrics[MetricExporterObservationValid] {
		if label(row, "scope") == "workload" && (label(row, LabelReason) != store.ReasonPermissionDenied || row.GetGauge().GetValue() != 0) {
			t.Fatalf("workload failure = %v", row)
		}
	}
	if len(metrics[MetricExporterObservationValid]) != 2 {
		t.Fatal("missing initial failure or duplicate reasons")
	}
	for _, tc := range []struct{ current, next, want string }{
		{"", "none", ""}, {"", "detail not in contract", store.ReasonAPIUnavailable},
		{store.ReasonUnresolvedOwner, store.ReasonInvalidDefinition, store.ReasonInvalidDefinition},
		{store.ReasonPermissionDenied, store.ReasonAPIUnavailable, store.ReasonPermissionDenied},
		{store.ReasonEvaluationFailed, store.ReasonAPIUnavailable, store.ReasonAPIUnavailable},
	} {
		if got := failureReason(tc.current, tc.next); got != tc.want {
			t.Errorf("failureReason(%q, %q) = %q, want %q", tc.current, tc.next, got, tc.want)
		}
	}
}

func TestRecoveryAndDeletionRemoveOldSeries(t *testing.T) {
	s := fixtureStore()
	registry := testGatherer(t, s, Options{Identity: true})
	s.SetPublicationFailure("status", store.ReasonPermissionDenied)
	if _, err := registry.Gather(); err != nil {
		t.Fatal(err)
	}
	s.SetPublicationFailure("status", "")
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, row := range family.Metric {
			if family.GetName() == MetricWorkloadStatus && label(row, LabelObservationValid) != "true" {
				t.Fatal("stale false status variant retained")
			}
			if family.GetName() == MetricExporterObservationValid && label(row, "scope") == "workload" {
				t.Fatal("recovered workload retained failure")
			}
		}
	}
	s.RemovePublication("w1")
	s.SetPublicationHealth(nil)
	families, err = registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("deleted workload retained current series: %v", families)
	}
}

type capturedSource struct{ capture func() store.CapturedView }

func (s capturedSource) Capture() store.CapturedView { return s.capture() }

func TestOneCaptureUsesFrozenTimeAndFailureState(t *testing.T) {
	s := fixtureStore()
	captures := 0
	source := capturedSource{capture: func() store.CapturedView {
		captures++
		view := s.Capture()
		s.SetPublicationFailure("status", store.ReasonPermissionDenied)
		s.BindPublication(nil, func() time.Time { return fixtureTime.Add(time.Hour) })
		return view
	}}
	metrics := gather(t, source, Options{Identity: true})
	if captures != 1 {
		t.Fatalf("captures = %d, want 1", captures)
	}
	if len(metrics[MetricWorkloadCreatedTimestamp]) != 1 {
		t.Fatal("creation used a later time")
	}
	for _, name := range []string{MetricPodWorkloadInfo, MetricWorkloadStatus} {
		for _, row := range metrics[name] {
			if label(row, LabelObservationValid) != "true" || label(row, LabelSubmittedBy) != "alice" {
				t.Fatalf("row ignored captured evidence: %v", row)
			}
		}
	}
}

func TestCollectionCancellationSkipsCapture(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	captures := 0
	registration, err := New(provider.Meter("cancel-test"), capturedSource{capture: func() store.CapturedView { captures++; return store.CapturedView{} }}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registration.Unregister(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect = %v, want canceled", err)
	}
	if captures != 0 {
		t.Fatalf("canceled collection captured %d times", captures)
	}
}
