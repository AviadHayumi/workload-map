// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"k8s.io/client-go/rest"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/config"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type runtimeStub struct {
	run   func(context.Context) error
	ready atomic.Bool
}

func (r *runtimeStub) Run(ctx context.Context) error { return r.run(ctx) }
func (r *runtimeStub) Ready() bool                   { return r.ready.Load() }

type shutdownReader struct {
	sdkmetric.Reader
	workerStopped *atomic.Bool
	source        *countedStore
	failure       error
	stopped       bool
	bounded       bool
	unregistered  bool
}

func (r *shutdownReader) Shutdown(ctx context.Context) error {
	r.stopped = r.workerStopped.Load()
	_, r.bounded = ctx.Deadline()
	before := r.source.captures.Load()
	var data metricdata.ResourceMetrics
	err := r.Collect(ctx, &data)
	r.unregistered = err == nil && r.source.captures.Load() == before
	return errors.Join(err, r.Reader.Shutdown(ctx), r.failure)
}

type countedStore struct {
	store.Interface
	captures atomic.Int32
}

func (s *countedStore) Capture() store.CapturedView {
	s.captures.Add(1)
	return s.Interface.Capture()
}

var _ = Describe("Application lifecycle", func() {
	for _, tc := range []struct {
		name                    string
		runtimeErr, shutdownErr error
	}{
		{name: "cancellation"},
		{name: "runtime failure", runtimeErr: errors.New("runtime failed")},
		{name: "provider failure", shutdownErr: errors.New("shutdown failed")},
		{name: "both failures", runtimeErr: errors.New("runtime failed"), shutdownErr: errors.New("shutdown failed")},
	} {
		It("registers before workers and cleans up after "+tc.name, func() {
			var stopped atomic.Bool
			source := &countedStore{Interface: store.New(store.Options{})}
			reader := &shutdownReader{Reader: sdkmetric.NewManualReader(), workerStopped: &stopped, source: source, failure: tc.shutdownErr}
			started := make(chan struct{})
			worker := &runtimeStub{run: func(ctx context.Context) error {
				var data metricdata.ResourceMetrics
				if err := reader.Collect(ctx, &data); err != nil {
					return err
				}
				close(started)
				if tc.runtimeErr == nil {
					<-ctx.Done()
				}
				stopped.Store(true)
				return tc.runtimeErr
			}}
			application := &Application{Runtime: worker, Store: source, Handler: http.NewServeMux(), provider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).NotTo(HaveOccurred())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- application.ServeListener(ctx, listener) }()
			Eventually(started).Should(BeClosed())
			Expect(source.captures.Load()).To(Equal(int32(1)))
			cancel()
			Eventually(done).Should(Receive(&err))
			if tc.runtimeErr == nil && tc.shutdownErr == nil {
				Expect(err).NotTo(HaveOccurred())
			}
			if tc.runtimeErr != nil {
				Expect(errors.Is(err, tc.runtimeErr)).To(BeTrue())
			}
			if tc.shutdownErr != nil {
				Expect(errors.Is(err, tc.shutdownErr)).To(BeTrue())
			}
			Expect(reader.stopped).To(BeTrue())
			Expect(reader.unregistered).To(BeTrue())
			Expect(reader.bounded).To(BeTrue())
		})
	}

	It("serves metrics and health before readiness and stops workers on HTTP failure", func() {
		application, err := New(config.DefaultConfig(), &rest.Config{Host: "http://127.0.0.1:1"})
		Expect(err).NotTo(HaveOccurred())
		stopped := make(chan struct{})
		worker := &runtimeStub{run: func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)
			return nil
		}}
		application.Runtime = worker
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- application.ServeListener(ctx, listener) }()
		client := &http.Client{Timeout: time.Second}
		for path, expected := range map[string]int{"/metrics": http.StatusOK, "/healthz": http.StatusOK, "/readyz": http.StatusServiceUnavailable} {
			response, err := client.Get("http://" + listener.Addr().String() + path)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.StatusCode).To(Equal(expected), path)
			Expect(response.Body.Close()).To(Succeed())
		}
		worker.ready.Store(true)
		response, err := client.Get("http://" + listener.Addr().String() + "/readyz")
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		Expect(response.Body.Close()).To(Succeed())
		Expect(listener.Close()).To(Succeed())
		Eventually(done).Should(Receive(&err))
		Expect(err).To(HaveOccurred())
		Expect(stopped).To(BeClosed())
	})
})
