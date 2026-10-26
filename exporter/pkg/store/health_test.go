// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"testing"
	"time"
)

func TestHealthFreshness(t *testing.T) {
	now := time.Unix(1792324800, 0)
	for _, tc := range []struct {
		name       string
		at         time.Time
		healthy    bool
		reason     string
		want       float64
		wantReason string
	}{
		{"fresh", now, true, ReasonNone, 1, ReasonNone},
		{"expiry", now.Add(time.Minute), true, ReasonNone, 0, ReasonAPIUnavailable},
		{"clock before read", now.Add(-time.Second), true, ReasonNone, 0, ReasonAPIUnavailable},
		{"denied", now, false, ReasonPermissionDenied, 0, ReasonPermissionDenied},
		{"unclassified failure", now, false, ReasonNone, 0, ReasonAPIUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := HealthRecord{Healthy: tc.healthy, Reason: tc.reason, VerifiedAt: now, MaxAge: time.Minute}
			value, reason := h.Status(tc.at)
			if value != tc.want || reason != tc.wantReason {
				t.Fatalf("Status=(%v,%q), want (%v,%q)", value, reason, tc.want, tc.wantReason)
			}
		})
	}
}
