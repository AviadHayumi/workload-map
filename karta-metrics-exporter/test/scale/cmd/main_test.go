// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/app"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

func TestDiagnostics(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Scale diagnostics") }

var _ = Describe("test diagnostics", func() {
	It("captures named queue metrics safely under concurrent updates", func() {
		p := &queueProvider{metrics: map[string]*queueMetric{}}
		d := diagnostic{queues: p}
		depth := p.NewDepthMetric("roots")
		latency := p.NewLatencyMetric("roots")
		var workers sync.WaitGroup
		for range 4 {
			workers.Go(func() {
				for range 100 {
					depth.Inc()
					latency.Observe(0.25)
					depth.Dec()
					_ = d.stats()
				}
			})
		}
		workers.Wait()
		result := d.stats()
		Expect(result.Queues["roots.depth"]["value"]).To(BeZero())
		Expect(result.Queues["roots.queue_latency_seconds"]["count"]).To(Equal(float64(400)))
		Expect(result.Queues["roots.queue_latency_seconds"]["sum"]).To(Equal(float64(100)))
		Expect(result.HeapAlloc).To(BeNumerically(">", 0))
	})
	It("reports exact publication versions and labels without a full metric scrape", func() {
		publications := store.New(store.Options{})
		publications.Publish(&store.Publication{Workload: store.WorkloadRecord{UID: "root", Ref: store.ObjectRef{ResourceVersion: "opaque-version"}}, PodPages: []*store.PodPage{{Rows: []store.PublishedPod{{Record: store.PodRecord{UID: "pod", Labels: map[string]string{"team": "team-v2"}}}}}}})
		handler := diagnostic{application: &app.Application{Components: app.Components{Store: publications}}, queues: &queueProvider{metrics: map[string]*queueMetric{}}}.handler()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/publications?uid=root&pod_uid=pod", nil))
		var result map[string]struct {
			ResourceVersion string                       `json:"resource_version"`
			Pods            map[string]map[string]string `json:"pods"`
		}
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result["root"].ResourceVersion).To(Equal("opaque-version"))
		Expect(result["root"].Pods["pod"]["team"]).To(Equal("team-v2"))
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/gc", nil))
		Expect(response.Code).To(Equal(http.StatusMethodNotAllowed))
	})
})
