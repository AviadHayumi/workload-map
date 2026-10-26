// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package telemetry

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	otlpcollector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

func TestPublishedCollectorOTLPWireContract(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			clearOTLPEnvironment(t)
			requests := make(chan receivedMetrics, 16)
			cfg := OTLPConfig{Protocol: protocol, Interval: time.Hour, Timeout: time.Second}
			if protocol == "grpc" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				server := grpc.NewServer()
				otlpcollector.RegisterMetricsServiceServer(server, &metricsReceiver{requests: requests})
				go func() { _ = server.Serve(listener) }()
				t.Cleanup(server.Stop)
				cfg.Endpoint = "http://" + listener.Addr().String()
			} else {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					request := &otlpcollector.ExportMetricsServiceRequest{}
					if err := proto.Unmarshal(body, request); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					requests <- receivedMetrics{request: request}
					w.Header().Set("Content-Type", "application/x-protobuf")
				}))
				t.Cleanup(server.Close)
				cfg.Endpoint = server.URL
			}
			provider, err := NewOTLPProvider(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := provider.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
			now := time.Date(2026, time.October, 20, 0, 0, 0, 0, time.UTC)
			s := store.New()
			s.BindPublication(nil, func() time.Time { return now })
			ref := store.WorkloadRef{Namespace: "team-a", Name: "training", Group: "batch", Version: "v1", Kind: "Job"}
			evidence := store.Evidence{ExpiresAt: now.Add(time.Minute)}
			publication := &store.Publication{
				Workload: store.WorkloadRecord{UID: "job-uid", Ref: ref, Karta: "batch-job-v1", KartaSource: "catalog", OwnerResolution: "resolved", HasStatus: true, CreatedTimestamp: 1792324800, Phases: []v1alpha1.ResourceStatus{v1alpha1.RunningStatus}},
				Status:   evidence, Creation: evidence, Membership: evidence,
			}
			publication = publication.WithPod("", &store.PublishedPod{Record: store.PodRecord{UID: "pod-uid", Namespace: "team-a", Name: "training-0", WorkloadUID: "job-uid", Workload: ref, OwnerResolution: "resolved", Component: "template", Instance: "worker", Replica: "0"}, Mapping: evidence})
			s.Publish(publication)
			s.SetPublicationHealth([]store.HealthRecord{{Scope: "resource_type", Ref: ref, Healthy: true, VerifiedAt: now, MaxAge: time.Minute}})
			registration, err := collector.New(provider.Meter("karta-exporter"), s, collector.Options{WorkloadPhases: []v1alpha1.ResourceStatus{v1alpha1.RunningStatus}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := registration.Unregister(); err != nil {
					t.Error(err)
				}
			})
			type sample struct {
				Labels map[string]string
				Value  float64
			}
			want := map[string]sample{
				"karta_pod_workload_info":                  {Value: 1, Labels: map[string]string{"namespace": "team-a", "workload_name": "training", "workload_group": "batch", "workload_version": "v1", "workload_kind": "Job", "workload_uid": "job-uid", "owner_resolution": "resolved", "pod_name": "training-0", "pod_uid": "pod-uid", "component": "template", "component_instance": "worker", "replica_group_id": "0", "observation_valid": "true"}},
				"karta_workload_status":                    {Value: 1, Labels: map[string]string{"namespace": "team-a", "workload_name": "training", "workload_group": "batch", "workload_version": "v1", "workload_kind": "Job", "workload_uid": "job-uid", "phase": "Running", "owner_resolution": "resolved", "karta": "batch-job-v1", "karta_source": "catalog", "observation_valid": "true"}},
				"karta_workload_created_timestamp_seconds": {Value: 1792324800, Labels: map[string]string{"namespace": "team-a", "workload_name": "training", "workload_uid": "job-uid"}},
				"karta_exporter_observation_valid":         {Value: 1, Labels: map[string]string{"scope": "resource_type", "namespace": "team-a", "resource_group": "batch", "resource_version": "v1", "resource_kind": "Job", "workload_name": "", "workload_uid": "", "reason": "none"}},
			}
			for i := 0; i < 3; i++ {
				if i == 2 {
					s.RemovePublication("job-uid")
					want = map[string]sample{"karta_exporter_observation_valid": want["karta_exporter_observation_valid"]}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				err := provider.ForceFlush(ctx)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				var request *otlpcollector.ExportMetricsServiceRequest
				select {
				case received := <-requests:
					request = received.request
				case <-time.After(3 * time.Second):
					t.Fatal("missing collector export")
				}
				got := make(map[string]sample)
				for _, resource := range request.ResourceMetrics {
					for _, scope := range resource.ScopeMetrics {
						for _, m := range scope.Metrics {
							points := m.GetGauge().GetDataPoints()
							if len(points) != 1 {
								t.Fatalf("%s: expected exactly one gauge point, got %v", m.Name, m)
							}
							if _, exists := got[m.Name]; exists {
								t.Fatalf("duplicate metric family %s", m.Name)
							}
							labels := make(map[string]string)
							for _, attr := range points[0].Attributes {
								labels[attr.Key] = attr.Value.GetStringValue()
							}
							got[m.Name] = sample{Labels: labels, Value: points[0].GetAsDouble()}
						}
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("export %d:\ngot %#v\nwant %#v", i, got, want)
				}
			}
		})
	}
}
