// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/app"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type countingStore struct {
	store.Interface
	captures atomic.Int32
}

func (s *countingStore) Capture() store.CapturedView {
	s.captures.Add(1)
	return s.Interface.Capture()
}
func (s *countingStore) Published(types.UID) *store.Publication {
	panic("diagnostics must use the captured view")
}

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
	It("optionally captures a heap profile after the same GC snapshot", func() {
		d := diagnostic{queues: &queueProvider{metrics: map[string]*queueMetric{}}}
		server := httptest.NewServer(d.handler())
		defer server.Close()
		for _, path := range []string{"/gc", "/gc?heap=1"} {
			response, err := http.Post(server.URL+path, "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			var result gcSnapshot
			Expect(json.NewDecoder(response.Body).Decode(&result)).To(Succeed())
			response.Body.Close()
			Expect(result.GCCompletedAt.Before(result.GCStartedAt)).To(BeFalse())
			Expect(result.SnapshotAt.Before(result.GCCompletedAt)).To(BeFalse())
			Expect(result.HeapAlloc).To(BeNumerically(">", 0))
			if path == "/gc" {
				Expect(result.HeapProfile).To(BeEmpty())
				continue
			}
			Expect(result.ProfileStartedAt.Before(result.SnapshotAt)).To(BeFalse())
			Expect(result.ProfileCompletedAt.Before(result.ProfileStartedAt)).To(BeFalse())
			reader, err := gzip.NewReader(bytes.NewReader(result.HeapProfile))
			Expect(err).NotTo(HaveOccurred())
			profile, err := io.ReadAll(reader)
			Expect(err).NotTo(HaveOccurred())
			Expect(reader.Close()).To(Succeed())
			Expect(profile).NotTo(BeEmpty())
		}
	})
	It("acknowledges only a coherent valid captured publication over HTTP", func() {
		now := time.Now()
		publications := &countingStore{Interface: store.New(store.Options{Now: func() time.Time { return now }})}
		proof := store.Evidence{ExpiresAt: now.Add(time.Minute), Dependencies: []string{"proof"}}
		publication := &store.Publication{Workload: store.WorkloadRecord{UID: "root", HasStatus: true, CreatedTimestamp: 1, Ref: store.ObjectRef{ResourceVersion: "opaque-version"}}, Creation: proof, Status: proof, PodPages: []*store.PodPage{{Rows: []store.PublishedPod{{Record: store.PodRecord{UID: "pod", Labels: map[string]string{"team": "team-v2"}}, Mapping: proof}}}}}
		publications.Publish(publication)
		server := httptest.NewServer(diagnostic{application: &app.Application{Components: app.Components{Store: publications}}, queues: &queueProvider{metrics: map[string]*queueMetric{}}}.handler())
		defer server.Close()
		tooMany, err := http.Get(server.URL + "/publications?" + strings.Repeat("uid=root&", 101))
		Expect(err).NotTo(HaveOccurred())
		Expect(tooMany.StatusCode).To(Equal(http.StatusBadRequest))
		tooMany.Body.Close()

		read := func() map[string]any {
			response, err := http.Get(server.URL + "/publications?uid=root")
			Expect(err).NotTo(HaveOccurred())
			defer response.Body.Close()
			var result map[string]map[string]any
			Expect(json.NewDecoder(response.Body).Decode(&result)).To(Succeed())
			return result["root"]
		}
		Expect(read()["valid"]).To(BeTrue())
		Expect(publications.captures.Load()).To(Equal(int32(1)))
		publications.SetPublicationFailure("proof", store.ReasonAPIUnavailable)
		Expect(read()["resource_version"]).To(Equal("opaque-version"))
		Expect(read()["valid"]).To(BeFalse())
		publications.SetPublicationFailure("proof", store.ReasonNone)
		now = now.Add(2 * time.Minute)
		Expect(read()["valid"]).To(BeFalse())
		now = now.Add(-2 * time.Minute)
		copy := *publication
		copy.PodPages = []*store.PodPage{{Rows: []store.PublishedPod{{Record: store.PodRecord{UID: "pod"}, Mapping: store.Evidence{Reason: store.ReasonUnresolvedOwner}}}}}
		publications.Publish(&copy)
		Expect(read()["valid"]).To(BeFalse())
		copy = *publication
		copy.Creation = store.Evidence{Reason: store.ReasonAPIUnavailable}
		publications.Publish(&copy)
		Expect(read()["valid"]).To(BeFalse())
		copy = *publication
		copy.FailureReasons = []string{"label_evaluation_failed"}
		publications.Publish(&copy)
		Expect(read()["valid"]).To(BeFalse())
		copy = *publication
		copy.Status = store.Evidence{Reason: store.ReasonEvaluationFailed}
		publications.Publish(&copy)
		Expect(read()["valid"]).To(BeFalse())
	})
})
