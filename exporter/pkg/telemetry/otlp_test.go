// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package telemetry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric"
	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type receivedMetrics struct {
	request       *collector.ExportMetricsServiceRequest
	authorization string
	path          string
}

type metricsReceiver struct {
	collector.UnimplementedMetricsServiceServer
	requests chan receivedMetrics
}

func (r *metricsReceiver) Export(ctx context.Context, req *collector.ExportMetricsServiceRequest) (*collector.ExportMetricsServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	auth := ""
	if values := md.Get("authorization"); len(values) > 0 {
		auth = values[0]
	}
	select {
	case r.requests <- receivedMetrics{request: req, authorization: auth}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &collector.ExportMetricsServiceResponse{}, nil
}

func TestOTLPWireCurrentState(t *testing.T) {
	for _, transport := range []string{"http/protobuf", "https", "mtls", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			clearOTLPEnvironment(t)
			requests := make(chan receivedMetrics, 16)
			cfg := OTLPConfig{Protocol: "http/protobuf", Interval: time.Hour, Timeout: time.Second, Headers: map[string]string{"authorization": "Bearer local-test"}}
			if transport == "grpc" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				server := grpc.NewServer()
				collector.RegisterMetricsServiceServer(server, &metricsReceiver{requests: requests})
				go func() { _ = server.Serve(listener) }()
				t.Cleanup(server.Stop)
				cfg.Protocol = "grpc"
				cfg.Endpoint = "http://" + listener.Addr().String()
			} else {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if transport == "mtls" && (r.TLS == nil || len(r.TLS.PeerCertificates) != 1) {
						t.Error("missing authenticated client certificate")
					}
					if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-protobuf" {
						t.Errorf("unexpected HTTP request: %s, %s", r.Method, r.Header.Get("Content-Type"))
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					req := &collector.ExportMetricsServiceRequest{}
					if err := proto.Unmarshal(body, req); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					requests <- receivedMetrics{request: req, authorization: r.Header.Get("Authorization"), path: r.URL.Path}
					w.Header().Set("Content-Type", "application/x-protobuf")
					w.WriteHeader(http.StatusOK)
				})
				server := httptest.NewUnstartedServer(handler)
				if transport == "https" || transport == "mtls" {
					var clientCert tls.Certificate
					if transport == "mtls" {
						key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
						if err != nil {
							t.Fatal(err)
						}
						template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
						der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
						if err != nil {
							t.Fatal(err)
						}
						cert, err := x509.ParseCertificate(der)
						if err != nil {
							t.Fatal(err)
						}
						clients := x509.NewCertPool()
						clients.AddCert(cert)
						server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clients}
						clientCert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
					}
					server.StartTLS()
					roots := x509.NewCertPool()
					roots.AddCert(server.Certificate())
					cfg.TLSConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
					if transport == "mtls" {
						cfg.TLSConfig.Certificates = []tls.Certificate{clientCert}
					}
				} else {
					server.Start()
				}
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
			meter := provider.Meter("wire-test")
			gauge, err := meter.Float64ObservableGauge("workload_value")
			if err != nil {
				t.Fatal(err)
			}
			var deleted atomic.Bool
			_, err = meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
				if !deleted.Load() {
					observer.ObserveFloat64(gauge, 42)
				}
				return nil
			}, gauge)
			if err != nil {
				t.Fatal(err)
			}
			keepalive, err := meter.Float64ObservableGauge("active_value")
			if err != nil {
				t.Fatal(err)
			}
			_, err = meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
				observer.ObserveFloat64(keepalive, 1)
				return nil
			}, keepalive)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if i == 2 {
					deleted.Store(true)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				err := provider.ForceFlush(ctx)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				var received receivedMetrics
				select {
				case received = <-requests:
				case <-time.After(3 * time.Second):
					t.Fatal("no OTLP export")
				}
				if received.authorization != "Bearer local-test" {
					t.Fatalf("authorization = %q", received.authorization)
				}
				if transport != "grpc" && received.path != "/v1/metrics" {
					t.Fatalf("path = %q", received.path)
				}
				found := false
				active := false
				for _, resource := range received.request.ResourceMetrics {
					for _, scope := range resource.ScopeMetrics {
						for _, m := range scope.Metrics {
							switch m.Name {
							case "workload_value":
								found = true
								points := m.GetGauge().GetDataPoints()
								if len(points) != 1 || points[0].GetAsDouble() != 42 {
									t.Fatalf("unexpected gauge: %v", m)
								}
							case "active_value":
								active = true
							}
						}
					}
				}
				if found != (i < 2) || !active {
					t.Fatalf("export %d: workload present=%t, active=%t", i, found, active)
				}
			}
		})
	}
}

func TestOTLPShutdownHonorsDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	provider, err := NewOTLPProvider(t.Context(), OTLPConfig{Protocol: "http/protobuf", Endpoint: server.URL, Interval: time.Hour, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	gauge, err := provider.Meter("shutdown-test").Float64Gauge("value")
	if err != nil {
		t.Fatal(err)
	}
	gauge.Record(t.Context(), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := provider.Shutdown(ctx); err == nil {
		t.Fatal("shutdown unexpectedly succeeded against stalled receiver")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown exceeded deadline: %v", elapsed)
	}
}

func TestOTLPPeriodicExport(t *testing.T) {
	clearOTLPEnvironment(t)
	exports := make(chan struct{}, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case exports <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	provider, err := NewOTLPProvider(t.Context(), OTLPConfig{Protocol: "http/protobuf", Endpoint: server.URL, Interval: 20 * time.Millisecond, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	gauge, err := provider.Meter("periodic-test").Float64ObservableGauge("current_value")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Meter("periodic-test").RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		observer.ObserveFloat64(gauge, 42)
		return nil
	}, gauge)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-exports:
		case <-time.After(3 * time.Second):
			t.Fatal("periodic reader did not export")
		}
	}
}

func TestOTLPProviderRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider, err := NewOTLPProvider(ctx, OTLPConfig{Protocol: "http/protobuf", Endpoint: "http://localhost:4318", Interval: time.Second, Timeout: time.Second})
	if !errors.Is(err, context.Canceled) || provider != nil {
		t.Fatalf("canceled construction returned provider=%v, error=%v", provider, err)
	}
}

func TestOTLPProviderRejectsExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	provider, err := NewOTLPProvider(ctx, OTLPConfig{Protocol: "grpc", Endpoint: "http://localhost:4317", Interval: time.Second, Timeout: time.Second})
	if !errors.Is(err, context.DeadlineExceeded) || provider != nil {
		t.Fatalf("expired construction returned provider=%v, error=%v", provider, err)
	}
}
