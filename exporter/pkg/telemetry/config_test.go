// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package telemetry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func clearOTLPEnvironment(t *testing.T) {
	t.Helper()
	for _, prefix := range []string{"OTEL_EXPORTER_OTLP_", "OTEL_EXPORTER_OTLP_METRICS_"} {
		for _, suffix := range []string{"PROTOCOL", "ENDPOINT", "HEADERS", "HEADERS_FILE", "CERTIFICATE", "CLIENT_CERTIFICATE", "CLIENT_KEY", "TIMEOUT", "INSECURE"} {
			t.Setenv(prefix+suffix, "")
		}
	}
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "")
	t.Setenv("OTEL_METRIC_EXPORT_TIMEOUT", "")
}

func TestOTLPConfigDefaultsAndPrecedence(t *testing.T) {
	clearOTLPEnvironment(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	cfg, err := OTLPConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Protocol != "http/protobuf" || cfg.Interval != 60*time.Second || cfg.Timeout != 30*time.Second || cfg.TLSConfig != nil || cfg.Endpoint != "http://localhost:4318/v1/metrics" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "https://localhost:9443/custom/metrics")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=generic")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "authorization=Bearer%20specific,x-key=a%2Cb%3Dc")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "250")
	t.Setenv("OTEL_METRIC_EXPORT_TIMEOUT", "125")
	cfg, err = OTLPConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Protocol != "http/protobuf" || cfg.Endpoint != "https://localhost:9443/custom/metrics" || cfg.Interval != 250*time.Millisecond || cfg.Timeout != 125*time.Millisecond {
		t.Fatalf("precedence failed: %+v", cfg)
	}
	if cfg.Headers["authorization"] != "Bearer specific" || cfg.Headers["x-key"] != "a,b=c" {
		t.Fatalf("headers = %v", cfg.Headers)
	}
}

func TestOTLPConfigHeadersFile(t *testing.T) {
	clearOTLPEnvironment(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	path := filepath.Join(t.TempDir(), "headers")
	if err := os.WriteFile(path, []byte("authorization=Bearer%20file"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS_FILE", path)
	cfg, err := OTLPConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Headers["authorization"] != "Bearer file" {
		t.Fatalf("headers=%v", cfg.Headers)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "authorization=direct")
	cfg, err = OTLPConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Headers["authorization"] != "direct" {
		t.Fatalf("direct header did not override file: %v", cfg.Headers)
	}
}

func TestOTLPConfigRejectsInvalidEnvironment(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"missing endpoint", "OTEL_EXPORTER_OTLP_ENDPOINT", ""},
		{"unsupported protocol", "OTEL_EXPORTER_OTLP_PROTOCOL", "http/json"},
		{"missing scheme", "OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4318"},
		{"unsupported scheme", "OTEL_EXPORTER_OTLP_ENDPOINT", "ftp://localhost:4318"},
		{"missing host", "OTEL_EXPORTER_OTLP_ENDPOINT", "http:///v1/metrics"},
		{"zero interval", "OTEL_METRIC_EXPORT_INTERVAL", "0"},
		{"negative interval", "OTEL_METRIC_EXPORT_INTERVAL", "-1"},
		{"fractional interval", "OTEL_METRIC_EXPORT_INTERVAL", "1.5"},
		{"duration interval", "OTEL_METRIC_EXPORT_INTERVAL", "1s"},
		{"zero timeout", "OTEL_METRIC_EXPORT_TIMEOUT", "0"},
		{"negative timeout", "OTEL_METRIC_EXPORT_TIMEOUT", "-1"},
		{"header newline", "OTEL_EXPORTER_OTLP_HEADERS", "authorization=abc%0D%0Ainjected"},
		{"invalid header escape", "OTEL_EXPORTER_OTLP_HEADERS", "authorization=%zz"},
		{"missing client key", "OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE", "/missing/cert.pem"},
		{"missing client certificate", "OTEL_EXPORTER_OTLP_CLIENT_KEY", "/missing/key.pem"},
		{"missing CA file", "OTEL_EXPORTER_OTLP_CERTIFICATE", "/missing/ca.pem"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clearOTLPEnvironment(t)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
			t.Setenv(tt.key, tt.value)
			if _, err := OTLPConfigFromEnv(); err == nil {
				t.Fatal("invalid environment accepted")
			}
		})
	}
}

func TestOTLPConfigValidate(t *testing.T) {
	for _, tt := range []struct {
		name   string
		modify func(*OTLPConfig)
	}{
		{"protocol", func(c *OTLPConfig) { c.Protocol = "http/json" }},
		{"grpc path", func(c *OTLPConfig) { c.Protocol = "grpc"; c.Endpoint = "http://localhost:4317/v1/metrics" }},
		{"credentials", func(c *OTLPConfig) { c.Endpoint = "http://user:password@localhost:4318" }},
		{"query", func(c *OTLPConfig) { c.Endpoint = "http://localhost:4318?token=secret" }},
		{"fragment", func(c *OTLPConfig) { c.Endpoint = "http://localhost:4318/#metrics" }},
		{"invalid port", func(c *OTLPConfig) { c.Endpoint = "http://localhost:99999" }},
		{"submillisecond interval", func(c *OTLPConfig) { c.Interval = time.Microsecond }},
		{"submillisecond timeout", func(c *OTLPConfig) { c.Timeout = time.Microsecond }},
		{"zero timeout", func(c *OTLPConfig) { c.Timeout = 0 }},
		{"negative interval", func(c *OTLPConfig) { c.Interval = -time.Second }},
		{"header name", func(c *OTLPConfig) { c.Headers = map[string]string{"bad name": "value"} }},
		{"header value", func(c *OTLPConfig) { c.Headers = map[string]string{"authorization": "value\r\n"} }},
		{"skip verification", func(c *OTLPConfig) {
			c.Endpoint = "https://localhost:4318"
			c.TLSConfig = &tls.Config{InsecureSkipVerify: true}
		}},
		{"TLS on plaintext", func(c *OTLPConfig) { c.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := OTLPConfig{Protocol: "http/protobuf", Endpoint: "http://localhost:4318", Interval: time.Second, Timeout: time.Second}
			tt.modify(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestOTLPConfigLoadsTLSFiles(t *testing.T) {
	clearOTLPEnvironment(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://localhost:4318")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", "/generic/path/must/not/be/used")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE", certPath)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_CLIENT_CERTIFICATE", certPath)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_CLIENT_KEY", keyPath)
	cfg, err := OTLPConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.RootCAs == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.MinVersion < tls.VersionTLS12 || len(cfg.TLSConfig.Certificates) != 1 {
		t.Fatalf("TLS configuration not loaded: %+v", cfg.TLSConfig)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: cfg.TLSConfig.RootCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("custom certificate is not trusted: %v", err)
	}
	if err := os.WriteFile(certPath, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OTLPConfigFromEnv(); err == nil {
		t.Fatal("invalid CA PEM accepted")
	}
}
