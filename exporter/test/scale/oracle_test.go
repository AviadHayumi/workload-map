// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package scale

import (
	"fmt"
	"strings"
	"testing"
)

func TestCasesRequireExplicitSupportedScale(t *testing.T) {
	for _, input := range []struct{ workloads, pods string }{{"", ""}, {"42", "1"}, {"1000", "2"}, {"bad", "0"}} {
		if _, err := Cases(input.workloads, input.pods); err == nil {
			t.Errorf("accepted unsupported scenario %+v", input)
		}
	}
	if cases, err := Cases("all", "all"); err != nil || len(cases) != 9 {
		t.Fatalf("full matrix: %v, %v", cases, err)
	}
}

func TestOracleRejectsLostOrDuplicatedIdentities(t *testing.T) {
	f := New(Case{Workloads: 1, Pods: 1})
	var text strings.Builder
	for _, phase := range []string{"Initializing", "Running", "Completed", "Failed", "Degraded", "Suspended", "Suspending", "Resuming", "Undefined"} {
		value := 0
		if phase == "Running" {
			value = 1
		}
		fmt.Fprintf(&text, "karta_workload_status{namespace=\"scale\",workload_name=\"workload-0\",workload_uid=\"workload-0-v0\",workload_group=\"batch\",workload_version=\"v1\",workload_kind=\"Job\",owner_resolution=\"resolved\",karta=\"scale-job\",karta_source=\"cluster\",observation_valid=\"true\",phase=\"%s\"} %d\n", phase, value)
	}
	text.WriteString("karta_exporter_observation_valid{scope=\"resource_type\",namespace=\"scale\",workload_name=\"\",workload_uid=\"\",resource_group=\"batch\",resource_version=\"v1\",resource_kind=\"Job\",reason=\"none\"} 1\n")
	text.WriteString("karta_workload_created_timestamp_seconds{namespace=\"scale\",workload_name=\"workload-0\",workload_uid=\"workload-0-v0\"} 1792324800\n")
	const pod = "karta_pod_workload_info{namespace=\"scale\",workload_name=\"workload-0\",workload_uid=\"workload-0-v0\",workload_group=\"batch\",workload_version=\"v1\",workload_kind=\"Job\",owner_resolution=\"resolved\",pod_name=\"pod-0-0\",pod_uid=\"pod-0-0-v0\",component=\"worker\",component_instance=\"instance-0\",replica_group_id=\"0\",observation_valid=\"true\"} 1\n"
	valid := text.String() + pod
	if counts, err := f.Verify(strings.NewReader(valid), false); err != nil || counts != (Counts{Status: 9, Created: 1, Health: 1, Pods: 1}) {
		t.Fatalf("valid fixture: %+v, %v", counts, err)
	}
	for name, invalid := range map[string]string{
		"missing Pod":           text.String(),
		"duplicate Pod":         valid + pod,
		"stale UID":             strings.ReplaceAll(valid, "workload-0-v0", "workload-0-v1"),
		"overflow":              valid + "karta_workload_status{otel_metric_overflow=\"true\"} 1\n",
		"wrong phase":           strings.Replace(valid, "phase=\"Running\"} 1", "phase=\"Running\"} 0", 1),
		"empty duplicate label": strings.Replace(valid, "namespace=\"scale\"", "namespace=\"\",namespace=\"scale\"", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.Verify(strings.NewReader(invalid), false); err == nil {
				t.Fatal("oracle accepted corrupted exposition")
			}
		})
	}
}
