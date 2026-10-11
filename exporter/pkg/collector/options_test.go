// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import "testing"

func TestParseOptions(t *testing.T) {
	tests := []struct {
		name, workload, pod, mode string
		wantWorkload, wantPod     int
		wantError                 bool
	}{
		{"defaults", "all", "all", "counts", 9, 5, false},
		{"selected", "Running,Failed", "Running", "both", 2, 1, false},
		{"disabled", "none", "none", "inventory", 0, 0, false},
		{"invalid state", "Ready", "all", "counts", 0, 0, true},
		{"invalid pod phase", "all", "Completed", "counts", 0, 0, true},
		{"duplicate", "Running,Running", "all", "counts", 0, 0, true},
		{"duplicate pod phase", "all", "Running,Running", "counts", 0, 0, true},
		{"empty", "", "all", "counts", 0, 0, true},
		{"mixed all", "all,Running", "all", "counts", 0, 0, true},
		{"invalid mode", "all", "all", "compact", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options, err := ParseOptions(tt.workload, tt.pod, tt.mode)
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tt.wantError)
			}
			if err != nil {
				return
			}
			if len(options.WorkloadPhases) != tt.wantWorkload || len(options.PodPhases) != tt.wantPod {
				t.Fatalf("unexpected phase selection: %+v", options)
			}
			if options.WorkloadPhases == nil || options.PodPhases == nil {
				t.Fatal("explicit none must remain distinguishable from defaults")
			}
		})
	}
}
