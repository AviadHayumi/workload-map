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
			if response.Code != http.StatusOK {
				t.Errorf("synced scrape status = %d, want 200", response.Code)
			}
		})
	}
	wg.Wait()
}
