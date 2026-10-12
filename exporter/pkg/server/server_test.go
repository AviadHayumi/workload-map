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

func TestConcurrentScrapesRespectInitialSync(t *testing.T) {
	var ready atomic.Bool
	s := New(":0", prometheus.NewRegistry(), ready.Load)
	before := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if before.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial scrape status = %d, want 503", before.Code)
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
