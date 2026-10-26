// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package telemetry

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

type OTLPConfig struct {
	Protocol  string
	Endpoint  string
	Interval  time.Duration
	Timeout   time.Duration
	Headers   map[string]string
	TLSConfig *tls.Config
}

// Validate rejects invalid configuration before starting the exporter.
func (c OTLPConfig) Validate() error {
	if c.Protocol != "http/protobuf" && c.Protocol != "grpc" {
		return errors.New("OTLP protocol must be http/protobuf or grpc")
	}
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return errors.New("OTLP endpoint must be an HTTP or HTTPS URL without credentials, query or fragment")
	}
	if port := endpoint.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return errors.New("OTLP endpoint port must be between 1 and 65535")
		}
	}
	if c.Protocol == "grpc" && endpoint.Path != "" && endpoint.Path != "/" {
		return errors.New("OTLP gRPC endpoint must not include a path")
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{{"interval", c.Interval}, {"timeout", c.Timeout}} {
		if field.value <= 0 || field.value%time.Millisecond != 0 {
			return fmt.Errorf("OTLP %s must be a positive whole-millisecond duration", field.name)
		}
	}
	for key, value := range c.Headers {
		if !httpguts.ValidHeaderFieldName(key) || !httpguts.ValidHeaderFieldValue(value) {
			return errors.New("OTLP headers contain an invalid name or value")
		}
		if c.Protocol == "grpc" {
			for _, char := range strings.ToLower(key) {
				if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
					return errors.New("OTLP gRPC header name contains an unsupported character")
				}
			}
		}
	}
	if c.TLSConfig != nil {
		if c.TLSConfig.InsecureSkipVerify {
			return errors.New("OTLP TLS certificate verification must be enabled")
		}
		if endpoint.Scheme != "https" {
			return errors.New("OTLP TLS configuration requires an HTTPS endpoint")
		}
	}
	return nil
}

// OTLPConfigFromEnv reads metric-specific OTEL settings before generic settings.
// A headers environment value takes precedence over the optional headers file.
func OTLPConfigFromEnv() (OTLPConfig, error) {
	config := OTLPConfig{
		Protocol: otlpEnv("PROTOCOL"),
		Endpoint: otlpEnv("ENDPOINT"),
		Interval: time.Minute,
		Timeout:  30 * time.Second,
		Headers:  make(map[string]string),
	}
	if config.Protocol == "" {
		config.Protocol = "http/protobuf"
	}
	if config.Protocol == "http/protobuf" && os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" {
		if endpoint, err := url.Parse(config.Endpoint); err == nil && endpoint.Hostname() != "" {
			endpoint.Path = path.Join(endpoint.Path, "/v1/metrics")
			config.Endpoint = endpoint.String()
		}
	}
	for _, field := range []struct {
		name  string
		value *time.Duration
	}{{"OTEL_METRIC_EXPORT_INTERVAL", &config.Interval}, {"OTEL_METRIC_EXPORT_TIMEOUT", &config.Timeout}} {
		if input := os.Getenv(field.name); input != "" {
			milliseconds, err := strconv.ParseInt(input, 10, 64)
			if err != nil || milliseconds <= 0 || milliseconds > math.MaxInt64/int64(time.Millisecond) {
				return OTLPConfig{}, fmt.Errorf("%s must be a positive integer number of milliseconds", field.name)
			}
			*field.value = time.Duration(milliseconds) * time.Millisecond
		}
	}
	headers := otlpEnv("HEADERS")
	if filename := otlpEnv("HEADERS_FILE"); headers == "" && filename != "" {
		data, err := os.ReadFile(filename)
		if err != nil {
			return OTLPConfig{}, fmt.Errorf("read OTLP headers file: %w", err)
		}
		headers = strings.TrimSpace(string(data))
	}
	if headers != "" {
		for _, entry := range strings.Split(headers, ",") {
			key, value, ok := strings.Cut(entry, "=")
			if !ok {
				return OTLPConfig{}, errors.New("OTLP headers must contain comma-separated key=value entries")
			}
			value, err := url.PathUnescape(strings.TrimSpace(value))
			if err != nil {
				return OTLPConfig{}, errors.New("OTLP header value has invalid percent encoding")
			}
			config.Headers[strings.TrimSpace(key)] = value
		}
	}
	caFile, certFile, keyFile := otlpEnv("CERTIFICATE"), otlpEnv("CLIENT_CERTIFICATE"), otlpEnv("CLIENT_KEY")
	if (certFile == "") != (keyFile == "") {
		return OTLPConfig{}, errors.New("OTLP client certificate and key must both be configured")
	}
	if caFile != "" || certFile != "" {
		config.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if caFile != "" {
		data, err := os.ReadFile(caFile)
		if err != nil {
			return OTLPConfig{}, fmt.Errorf("read OTLP CA certificate: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return OTLPConfig{}, fmt.Errorf("load system CA certificates: %w", err)
		}
		if !roots.AppendCertsFromPEM(data) {
			return OTLPConfig{}, errors.New("OTLP CA file contains no certificates")
		}
		config.TLSConfig.RootCAs = roots
	}
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return OTLPConfig{}, fmt.Errorf("load OTLP client certificate and key: %w", err)
		}
		config.TLSConfig.Certificates = []tls.Certificate{cert}
	}
	if err := config.Validate(); err != nil {
		return OTLPConfig{}, err
	}
	return config, nil
}

func otlpEnv(name string) string {
	if value := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_" + name); value != "" {
		return value
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_" + name)
}
