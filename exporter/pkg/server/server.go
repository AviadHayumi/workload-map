// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server serves /metrics, /healthz, and /readyz. Readiness is gated so a
// half-built store is never scraped as truth: Prometheus sees the target
// down instead of plausible zeros.
type Server struct {
	httpServer *http.Server
}

func New(addr string, gatherer prometheus.Gatherer, ready func() bool) *Server {
	mux := http.NewServeMux()
	metricsHandler := promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
	var everReady atomic.Bool
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		// a half-built store must fail the scrape visibly, not serve
		// plausible zeros; once synced, keep serving forever
		if !everReady.Load() {
			if !ready() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			everReady.Store(true)
		}
		metricsHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
}

// Run serves until the context is canceled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("metrics server failed: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	}
}
