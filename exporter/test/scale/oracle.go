// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package scale

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
)

type Response struct {
	HeaderMap http.Header
	Code      int
	Bytes     int64
	Sink      io.Writer
}

type Counts struct {
	Status  int
	Created int
	Health  int
	Pods    int
}

func (r *Response) Header() http.Header {
	if r.HeaderMap == nil {
		r.HeaderMap = make(http.Header)
	}
	return r.HeaderMap
}

func (r *Response) WriteHeader(code int) {
	if r.Code == 0 {
		r.Code = code
	}
}

func (r *Response) Write(p []byte) (int, error) {
	if r.Code == 0 {
		r.Code = http.StatusOK
	}
	r.Bytes += int64(len(p))
	if r.Sink != nil {
		return r.Sink.Write(p)
	}
	return len(p), nil
}

func Request() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Accept", "text/plain; version=0.0.4")
	return r
}

// VerifyHandler streams the encoded response, avoiding another full payload copy.
// Parsing is deliberately excluded from the timed benchmark path.
func (f *Fixture) VerifyHandler(handler http.Handler, allowEitherPhase bool) (Counts, int64, error) {
	reader, writer := io.Pipe()
	type result struct {
		counts Counts
		err    error
	}
	done := make(chan result, 1)
	go func() {
		counts, err := f.Verify(reader, allowEitherPhase)
		_ = reader.CloseWithError(err)
		done <- result{counts, err}
	}()
	response := &Response{Sink: writer}
	handler.ServeHTTP(response, Request())
	_ = writer.Close()
	got := <-done
	if response.Code != http.StatusOK {
		return got.counts, response.Bytes, fmt.Errorf("HTTP status %d", response.Code)
	}
	return got.counts, response.Bytes, got.err
}

// Verify checks every identity and phase against fixture inputs, without using
// collector label constants, gauge builders, or store snapshots as an oracle.
// The parser accepts the controlled ASCII fixture values, not arbitrary metrics.
func (f *Fixture) Verify(reader io.Reader, allowEitherPhase bool) (Counts, error) {
	phases := []string{"Initializing", "Running", "Completed", "Failed", "Degraded", "Suspended", "Suspending", "Resuming", "Undefined"}
	seenStatus := make([]uint16, f.Case.Workloads)
	positiveStatus := make([]int, f.Case.Workloads)
	seenCreation := make([]bool, f.Case.Workloads)
	seenPods := make([]bool, f.Case.Workloads*f.Case.Pods)
	counts := Counts{}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, "{")
		end := strings.LastIndexByte(rest, '}')
		if !ok || end < 0 {
			return counts, fmt.Errorf("unexpected exposition line %q", line)
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(rest[end+1:]), 64)
		if err != nil || (name != "karta_workload_created_timestamp_seconds" && value != 0 && value != 1) {
			return counts, fmt.Errorf("invalid gauge value in %q", line)
		}
		labels := make(map[string]string, 12)
		for _, field := range strings.Split(rest[:end], ",") {
			key, quoted, ok := strings.Cut(field, "=")
			val, err := strconv.Unquote(quoted)
			_, duplicate := labels[key]
			if !ok || err != nil || duplicate {
				return counts, fmt.Errorf("invalid or repeated label %q", field)
			}
			labels[key] = val
		}
		if name == "karta_exporter_observation_valid" {
			if len(labels) != 8 || labels["scope"] != "resource_type" || labels["namespace"] != "scale" || labels["resource_group"] != "batch" || labels["resource_version"] != "v1" || labels["resource_kind"] != "Job" || labels["workload_name"] != "" || labels["workload_uid"] != "" || labels["reason"] != "none" || value != 1 || counts.Health != 0 {
				return counts, fmt.Errorf("incorrect or duplicate type health: %v", labels)
			}
			counts.Health++
			continue
		}
		identity, ok := strings.CutPrefix(labels["workload_uid"], "workload-")
		id, generation, split := strings.Cut(identity, "-v")
		i, idErr := strconv.Atoi(id)
		g, generationErr := strconv.Atoi(generation)
		if !ok || !split || idErr != nil || generationErr != nil || i < 0 || i >= f.Case.Workloads || !f.Active[i] || g != f.Generation[i] {
			return counts, fmt.Errorf("unexpected workload identity %q", labels["workload_uid"])
		}
		if labels["namespace"] != "scale" || labels["workload_name"] != "workload-"+id {
			return counts, fmt.Errorf("incorrect root labels for %q", labels["workload_uid"])
		}
		switch name {
		case "karta_workload_status":
			if len(labels) != 11 || labels["observation_valid"] != "true" || labels["workload_group"] != "batch" || labels["workload_version"] != "v1" || labels["workload_kind"] != "Job" || labels["owner_resolution"] != "resolved" || labels["karta"] != "scale-job" || labels["karta_source"] != "cluster" {
				return counts, fmt.Errorf("incorrect status labels: %v", labels)
			}
			phaseIndex := -1
			for p, phase := range phases {
				if phase == labels["phase"] {
					phaseIndex = p
				}
			}
			if phaseIndex < 0 || seenStatus[i]&(1<<phaseIndex) != 0 {
				return counts, fmt.Errorf("unknown or duplicate phase for %q", labels["workload_uid"])
			}
			seenStatus[i] |= 1 << phaseIndex
			expected := "Running"
			if (i+g)%2 != 0 {
				expected = "Failed"
			}
			if value == 1 {
				positiveStatus[i]++
			}
			if allowEitherPhase {
				if value == 1 && labels["phase"] != "Running" && labels["phase"] != "Failed" {
					return counts, fmt.Errorf("invalid concurrent phase %q", labels["phase"])
				}
			} else if (value == 1) != (labels["phase"] == expected) {
				return counts, fmt.Errorf("wrong phase value for %q: %s=%v", labels["workload_uid"], labels["phase"], value)
			}
			counts.Status++
		case "karta_workload_created_timestamp_seconds":
			if len(labels) != 3 || value != 1792324800 || seenCreation[i] {
				return counts, fmt.Errorf("incorrect or duplicate creation time: %v", labels)
			}
			seenCreation[i] = true
			counts.Created++
		case "karta_pod_workload_info":
			if len(labels) != 13 || labels["observation_valid"] != "true" || labels["workload_group"] != "batch" || labels["workload_version"] != "v1" || labels["workload_kind"] != "Job" || labels["owner_resolution"] != "resolved" || labels["component"] != "worker" || value != 1 {
				return counts, fmt.Errorf("incorrect Pod labels: %v", labels)
			}
			p, err := strconv.Atoi(labels["replica_group_id"])
			if err != nil || p < 0 || p >= f.Case.Pods {
				return counts, fmt.Errorf("invalid Pod replica %q", labels["replica_group_id"])
			}
			podName := fmt.Sprintf("pod-%d-%d", i, p)
			if labels["pod_name"] != podName || labels["pod_uid"] != fmt.Sprintf("%s-v%d", podName, g) || labels["component_instance"] != fmt.Sprintf("instance-%d", p%2) || seenPods[i*f.Case.Pods+p] {
				return counts, fmt.Errorf("incorrect or duplicate Pod identity: %v", labels)
			}
			seenPods[i*f.Case.Pods+p] = true
			counts.Pods++
		default:
			return counts, fmt.Errorf("unexpected metric family %q", name)
		}
	}
	if err := scanner.Err(); err != nil {
		return counts, err
	}
	if counts.Health != 1 {
		return counts, fmt.Errorf("missing resource-type health")
	}
	for i, active := range f.Active {
		if !active {
			continue
		}
		if seenStatus[i] != (1<<len(phases))-1 || positiveStatus[i] != 1 || !seenCreation[i] {
			return counts, fmt.Errorf("missing dense status or creation for workload %d", i)
		}
		for p := range f.Case.Pods {
			if !seenPods[i*f.Case.Pods+p] {
				return counts, fmt.Errorf("missing Pod %d/%d", i, p)
			}
		}
	}
	return counts, nil
}
