// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMetricsRemainVisibleBeforeInitialSync(t *testing.T) {
	var ready atomic.Bool
	s := New(":0", prometheus.NewRegistry(), ready.Load)
	before := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if before.Code != http.StatusOK {
		t.Fatalf("initial scrape status = %d, want 200", before.Code)
	}
	notReady := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatal("readiness must remain false before sync")
	}
	ready.Store(true)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			response := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if response.Code != http.StatusOK && response.Code != http.StatusServiceUnavailable {
				t.Errorf("synced scrape status = %d, want 200 or busy 503", response.Code)
			}
		})
	}
	wg.Wait()
}

func TestConcurrentScrapeIsBoundedAndHealthStaysResponsive(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	gatherer := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil, nil
	})
	s := New(":0", gatherer, func() bool { return true })
	done := make(chan struct{})
	go func() {
		defer close(done)
		response := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if response.Code != http.StatusOK {
			t.Errorf("first scrape = %d, want 200", response.Code)
		}
	}()
	<-entered
	for _, path := range []string{"/metrics", "/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		want := http.StatusOK
		if path == "/metrics" {
			want = http.StatusServiceUnavailable
		}
		if response.Code != want {
			t.Errorf("%s during scrape = %d, want %d", path, response.Code, want)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("overlapping collection count = %d, want 1", calls.Load())
	}
	close(release)
	<-done
	retry := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(retry, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if retry.Code != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("retry status=%d, collections=%d, want 200 and 2", retry.Code, calls.Load())
	}
}
