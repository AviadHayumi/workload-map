// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import "testing"

func TestParseOptions(t *testing.T) {
	for _, tc := range []struct {
		value   string
		count   int
		invalid bool
	}{
		{value: "all", count: 9}, {value: "Running,Failed", count: 2}, {value: "none"},
		{value: "Ready", invalid: true}, {value: "", invalid: true}, {value: "Running,Running", invalid: true}, {value: "all,Running", invalid: true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			options, err := ParseOptions(tc.value)
			if (err != nil) != tc.invalid {
				t.Fatalf("error=%v, invalid=%v", err, tc.invalid)
			}
			if err == nil && (options.WorkloadPhases == nil || len(options.WorkloadPhases) != tc.count) {
				t.Fatalf("unexpected phases %v", options.WorkloadPhases)
			}
		})
	}
}
