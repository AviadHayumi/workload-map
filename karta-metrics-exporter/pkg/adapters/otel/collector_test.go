// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package otel

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	exporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type countingCapturer struct {
	store.Capturer
	calls int
}

func (c *countingCapturer) Capture() store.CapturedView { c.calls++; return c.Capturer.Capture() }
func TestCollector(t *testing.T)                        { RegisterFailHandler(Fail); RunSpecs(t, "OTel Collector") }

var _ = Describe("Prometheus pull collection", func() {
	var s store.Interface
	var p *store.Publication
	var now time.Time
	var source *countingCapturer
	var scrape func() string
	BeforeEach(func() {
		now = time.Unix(100, 0)
		s = store.New(store.Options{Now: func() time.Time { return now }})
		evidence := store.Evidence{ExpiresAt: now.Add(time.Minute), Dependencies: []string{"root"}}
		p = &store.Publication{Workload: store.WorkloadRecord{UID: "root", Ref: store.ObjectRef{Namespace: "ns", Name: "job", GVK: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}}, OwnerResolution: store.Resolved, HasStatus: true, Phases: []string{"Running", "Degraded"}, CreatedTimestamp: 50}, Status: evidence, Creation: evidence, PodPages: []*store.PodPage{{Rows: []store.PublishedPod{{Record: store.PodRecord{UID: "pod", Name: "worker", OwnerResolution: store.Resolved}, Mapping: evidence}}}}}
		registry := prom.NewRegistry()
		reader, err := exporter.New(exporter.WithRegisterer(registry), exporter.WithoutTargetInfo(), exporter.WithoutScopeInfo())
		Expect(err).NotTo(HaveOccurred())
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		DeferCleanup(func() { Expect(provider.Shutdown(context.Background())).To(Succeed()) })
		source = &countingCapturer{Capturer: s}
		registration, err := New().Register(provider.Meter("test"), source)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(registration.Unregister()).To(Succeed()) })
		handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
		scrape = func() string {
			before := source.calls
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
			Expect(response.Code).To(Equal(200))
			Expect(source.calls).To(Equal(before + 1))
			return response.Body.String()
		}
	})
	It("emits all nine phases, four families and arbitrary saved label maps", func() {
		labels := map[string]string{}
		for i := 0; i < 20; i++ {
			labels[fmt.Sprintf("custom_%02d", i)] = "saved"
		}
		p.Workload.StatusLabels = labels
		p.PodPages[0].Rows[0].Record.Labels = labels
		s.Publish(p)
		s.SetPublicationHealth([]store.HealthRecord{{GVK: p.Workload.Ref.GVK, Namespace: "ns", Evidence: p.Status}})
		output := scrape()
		for _, name := range []string{MetricPodWorkloadInfo, MetricWorkloadStatus, MetricWorkloadCreatedTimestamp, MetricExporterObservationValid} {
			Expect(output).To(ContainSubstring(name + "{"))
		}
		Expect(strings.Count(output, MetricWorkloadStatus+"{")).To(Equal(9))
		Expect(output).To(ContainSubstring(`custom_19="saved"`))
		Expect(output).To(ContainSubstring(`phase="Running"`))
		Expect(output).To(ContainSubstring(`reason="none"`))
		Expect(output).NotTo(ContainSubstring(`scope="workload"`))
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, MetricWorkloadStatus+"{") {
				expected := " 0"
				if strings.Contains(line, `phase="Running"`) || strings.Contains(line, `phase="Degraded"`) {
					expected = " 1"
				}
				Expect(line).To(HaveSuffix(expected))
			}
		}
	})
	It("preserves built-in identity and keeps custom labels off creation and health", func() {
		p.Workload.StatusLabels = map[string]string{"namespace": "wrong", "team": "status"}
		p.PodPages[0].Rows[0].Record.Labels = map[string]string{"namespace": "wrong", "team": "pod"}
		s.Publish(p)
		s.SetPublicationHealth([]store.HealthRecord{{Namespace: "ns", Evidence: p.Status}})
		output := scrape()
		Expect(output).NotTo(ContainSubstring(`namespace="wrong"`))
		for _, line := range strings.Split(output, "\n") {
			switch {
			case strings.HasPrefix(line, MetricWorkloadCreatedTimestamp+"{"), strings.HasPrefix(line, MetricExporterObservationValid+"{"):
				Expect(line).NotTo(ContainSubstring("team="))
			case strings.HasPrefix(line, MetricWorkloadStatus+"{"):
				Expect(line).To(ContainSubstring(`team="status"`))
			case strings.HasPrefix(line, MetricPodWorkloadInfo+"{"):
				Expect(line).To(ContainSubstring(`team="pod"`))
			}
		}
	})
	It("omits status when unavailable and reports resource dependency failures", func() {
		p.Workload.HasStatus = false
		s.Publish(p)
		s.SetPublicationHealth([]store.HealthRecord{{Namespace: "ns", Evidence: store.Evidence{ExpiresAt: now.Add(time.Minute), Dependencies: []string{"discovery"}}}})
		s.SetPublicationFailure("discovery", store.ReasonPermissionDenied)
		output := scrape()
		Expect(output).NotTo(ContainSubstring(MetricWorkloadStatus + "{"))
		Expect(output).To(ContainSubstring(`reason="permission_denied"`))
		Expect(output).To(ContainSubstring(`scope="resource_type"`))
		Expect(output).NotTo(ContainSubstring(`scope="workload"`))
	})

	It("removes old label and UID series on replacement and deletion", func() {
		p.Workload.StatusLabels = map[string]string{"team": "old"}
		s.Publish(p)
		Expect(scrape()).To(ContainSubstring(`team="old"`))
		next := *p
		next.Workload.StatusLabels = map[string]string{"team": "new"}
		s.Publish(&next)
		output := scrape()
		Expect(output).NotTo(ContainSubstring(`team="old"`))
		Expect(output).To(ContainSubstring(`team="new"`))
		s.RemovePublication("root")
		output = scrape()
		Expect(output).NotTo(ContainSubstring(`workload_uid="root"`))
		Expect(output).NotTo(ContainSubstring(`pod_uid="pod"`))
	})
	It("works without label rules and omits unknown or invalid creation timestamps", func() {
		for _, timestamp := range []int64{0, -1} {
			next := *p
			next.Workload.CreatedTimestamp = timestamp
			s.Publish(&next)
			output := scrape()
			Expect(output).To(ContainSubstring(MetricPodWorkloadInfo + "{"))
			Expect(output).NotTo(ContainSubstring(MetricWorkloadCreatedTimestamp + "{"))
		}
		next := *p
		next.Creation.Reason = store.ReasonPermissionDenied
		s.Publish(&next)
		Expect(scrape()).NotTo(ContainSubstring(MetricWorkloadCreatedTimestamp + "{"))
	})
	It("retains invalid observations and expires creation without changing phase values", func() {
		s.Publish(p)
		s.SetPublicationFailure("root", store.ReasonPermissionDenied)
		output := scrape()
		Expect(output).To(ContainSubstring(`observation_valid="false"`))
		Expect(output).NotTo(ContainSubstring(`observation_valid="true"`))
		Expect(output).To(ContainSubstring(`reason="permission_denied"`))
		Expect(output).NotTo(ContainSubstring(MetricWorkloadCreatedTimestamp + "{"))
		s.SetPublicationFailure("root", "")
		Expect(scrape()).To(ContainSubstring(`observation_valid="true"`))
		now = p.Status.ExpiresAt
		output = scrape()
		Expect(output).To(ContainSubstring(`reason="api_unavailable"`))
		Expect(output).NotTo(ContainSubstring(MetricWorkloadCreatedTimestamp + "{"))
	})
	It("keeps creation independent of status and bounds failure reasons", func() {
		p.Status.Reason = store.ReasonEvaluationFailed
		p.FailureReasons = []string{"unexpected detail", store.ReasonPermissionDenied}
		s.Publish(p)
		output := scrape()
		Expect(output).To(ContainSubstring(MetricWorkloadCreatedTimestamp + "{"))
		Expect(output).To(ContainSubstring(`reason="permission_denied"`))
		Expect(output).NotTo(ContainSubstring("unexpected detail"))
	})
})
