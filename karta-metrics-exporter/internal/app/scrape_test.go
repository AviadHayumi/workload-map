// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/config"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type blockedCapturer struct {
	store.Capturer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (c *blockedCapturer) Capture() store.CapturedView {
	c.calls.Add(1)
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Capturer.Capture()
}

var _ = Describe("Production HTTP scrape concurrency", func() {
	It("rejects overlap and preserves every UID without overflow in accepted scrapes", func() {
		application, err := New(config.DefaultConfig(), &rest.Config{Host: "http://127.0.0.1:1"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(application.provider.Shutdown(context.Background())).To(Succeed()) })
		now := time.Now()
		evidence := store.Evidence{ExpiresAt: now.Add(time.Hour)}
		expected := map[string]string{}
		for root := 0; root < 2; root++ {
			uid := types.UID(fmt.Sprintf("root-%d", root))
			rows := make([]store.PublishedPod, 2501)
			for i := range rows {
				podUID := fmt.Sprintf("pod-%d-%d", root, i)
				expected[podUID] = string(uid)
				rows[i] = store.PublishedPod{Record: store.PodRecord{UID: types.UID(podUID), Name: fmt.Sprintf("worker-%d", i), Namespace: "ns", OwnerResolution: store.Resolved}, Mapping: evidence}
			}
			application.Components.Store.Publish(&store.Publication{Workload: store.WorkloadRecord{UID: uid, Ref: store.ObjectRef{Namespace: "ns", Name: "same-name", GVK: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}}, OwnerResolution: store.Resolved, HasStatus: true, Phases: []string{"Running"}, CreatedTimestamp: now.Unix()}, Status: evidence, Creation: evidence, PodPages: store.BuildPodPages(rows, nil)})
		}
		source := &blockedCapturer{Capturer: application.Components.Store, entered: make(chan struct{}), release: make(chan struct{})}
		registration, err := application.Components.Registrar.Register(application.provider.Meter("scrape-test"), source)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(registration.Unregister()).To(Succeed()) })
		server := httptest.NewServer(application.Handler)
		DeferCleanup(server.Close)
		var unblock sync.Once
		release := func() { unblock.Do(func() { close(source.release) }) }
		DeferCleanup(release)
		client := &http.Client{Timeout: 10 * time.Second}
		type result struct {
			response *http.Response
			err      error
		}
		first := make(chan result, 1)
		go func() { response, err := client.Get(server.URL + "/metrics"); first <- result{response, err} }()
		Eventually(source.entered).Should(BeClosed())
		rejected, err := client.Get(server.URL + "/metrics")
		Expect(err).NotTo(HaveOccurred())
		Expect(rejected.StatusCode).To(Equal(http.StatusServiceUnavailable))
		body, err := io.ReadAll(rejected.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(rejected.Body.Close()).To(Succeed())
		Expect(string(body)).To(ContainSubstring("Limit of concurrent requests reached"))
		Expect(source.calls.Load()).To(Equal(int32(1)))
		release()
		var accepted result
		Eventually(first, 10*time.Second).Should(Receive(&accepted))
		Expect(accepted.err).NotTo(HaveOccurred())
		verify := func(response *http.Response) {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.Body.Close()).To(Succeed())
			Expect(string(body)).NotTo(ContainSubstring("overflow"))
			parser := expfmt.NewTextParser(model.UTF8Validation)
			families, err := parser.TextToMetricFamilies(strings.NewReader(string(body)))
			Expect(err).NotTo(HaveOccurred())
			Expect(families).To(HaveKey("karta_pod_workload_info"))
			Expect(families).To(HaveKey("karta_workload_status"))
			Expect(families).To(HaveKey("karta_workload_created_timestamp_seconds"))
			actual := map[string]string{}
			for _, metric := range families["karta_pod_workload_info"].Metric {
				labels := map[string]string{}
				for _, pair := range metric.Label {
					labels[pair.GetName()] = pair.GetValue()
				}
				Expect(actual).NotTo(HaveKey(labels["pod_uid"]))
				actual[labels["pod_uid"]] = labels["workload_uid"]
				Expect(labels["observation_valid"]).To(Equal("true"))
				Expect(metric.GetGauge().GetValue()).To(Equal(float64(1)))
			}
			Expect(actual).To(Equal(expected))
			Expect(families["karta_workload_status"].Metric).To(HaveLen(18))
			Expect(families["karta_workload_created_timestamp_seconds"].Metric).To(HaveLen(2))
		}
		verify(accepted.response)
		Expect(source.calls.Load()).To(Equal(int32(1)))
		subsequent, err := client.Get(server.URL + "/metrics")
		Expect(err).NotTo(HaveOccurred())
		verify(subsequent)
		Expect(source.calls.Load()).To(Equal(int32(2)))
	})
})
