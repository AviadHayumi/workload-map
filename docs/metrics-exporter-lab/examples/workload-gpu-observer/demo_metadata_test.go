// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetadataIndependentAndFailClosed(t *testing.T) {
	now := time.Now()
	d := newDemoMetadata("demo", 25*time.Second)
	registry := prometheus.NewRegistry()
	registry.MustRegister(d)
	round := demoRound{Version: 1, ID: "round", Started: now.Add(-time.Minute), Ends: now.Add(9 * time.Minute), Updated: now, Status: "partial", Assignments: []demoAssignment{{Namespace: "demo", UID: "root", Scenario: "quiet"}}}
	body, _ := json.Marshal(round)
	d.update(string(body), now)
	families, err := registry.Gather()
	if err != nil || len(families) != 6 {
		t.Fatalf("valid metadata: %v families %d", err, len(families))
	}
	for _, body := range []string{"not json", `{"version":1}`, string(body)} {
		at := now
		if body[0] == '{' && len(body) > 50 {
			at = now.Add(-30 * time.Second)
		}
		d.update(body, at)
		families, err = registry.Gather()
		if err != nil || len(families) != 1 || families[0].Metric[0].GetGauge().GetValue() != 0 {
			t.Fatal("invalid/stale intent must be omitted, not measured as activity")
		}
	}
}
