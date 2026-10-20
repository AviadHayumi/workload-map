// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"slices"
	"testing"
)

func TestSubmittedByMetricContract(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := fixtureStore()
		baseline := gather(t, s, Options{})
		metrics := gather(t, s, Options{Identity: enabled})
		if len(metrics) != 4 {
			t.Fatalf("published %d families, want 4", len(metrics))
		}
		for name, rows := range metrics {
			if len(rows) != len(baseline[name]) {
				t.Fatalf("%s sample count changed", name)
			}
			for _, row := range rows {
				want := make([]string, 0, len(baseline[name][0].Label)+1)
				for _, pair := range baseline[name][0].Label {
					want = append(want, pair.GetName())
				}
				if enabled && (name == MetricPodWorkloadInfo || name == MetricWorkloadStatus) {
					want = append(want, LabelSubmittedBy)
					if label(row, LabelSubmittedBy) != "alice" {
						t.Fatalf("%s did not use published submitter", name)
					}
				}
				var got []string
				for _, pair := range row.Label {
					got = append(got, pair.GetName())
				}
				slices.Sort(want)
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Fatalf("%s labels = %v, want %v", name, got, want)
				}
			}
		}
	}
}

func TestCollectorUsesPublishedSubmitterAgreement(t *testing.T) {
	s := fixtureStore()
	for _, submittedBy := range []string{"alice", ""} {
		p := *s.Published("w1")
		p.SubmittedBy = submittedBy
		s.Publish(&p)
		metrics := gather(t, s, Options{Identity: true})
		for _, name := range []string{MetricPodWorkloadInfo, MetricWorkloadStatus} {
			for _, row := range metrics[name] {
				if got := label(row, LabelSubmittedBy); got != submittedBy {
					t.Fatalf("%s submitted_by = %q, want %q", name, got, submittedBy)
				}
			}
		}
	}
}
