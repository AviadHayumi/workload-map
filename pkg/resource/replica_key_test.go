// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

func TestReplicaKeyApplicability(t *testing.T) {
	for _, tc := range []struct {
		name, query, want string
		found, wantError  bool
	}{
		{name: "explicit opt out", query: "empty"},
		{name: "conditional opt out", query: `if .metadata.labels.group then .metadata.labels.group else empty end`},
		{name: "numeric zero", query: "0", want: "0", found: true},
		{name: "string group", query: `"group-1"`, want: "group-1", found: true},
		{name: "missing required key", query: ".metadata.labels.group", wantError: true},
		{name: "empty string", query: `""`, wantError: true},
		{name: "multiple keys", query: `"0", "1"`, wantError: true},
		{name: "evaluation error", query: `error("invalid group")`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := NewPodQuerier(&corev1.Pod{}).ExtractReplicaKey(t.Context(), &v1alpha1.ReplicaSelector{KeyPath: tc.query})
			if (err != nil) != tc.wantError || got != tc.want || found != tc.found {
				t.Fatalf("ExtractReplicaKey(%q) = (%q, %t, %v), want (%q, %t, error=%t)", tc.query, got, found, err, tc.want, tc.found, tc.wantError)
			}
		})
	}
}
