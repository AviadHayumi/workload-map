// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOptionsValidatesResourceRuleEntries(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		wantValid  bool
	}{
		{"core", `{"apiGroups":[""],"apiVersions":["v1"],"resources":["pods"]}`, true},
		{"wildcards", `{"apiGroups":["*"],"apiVersions":["*"],"resources":["*"]}`, true},
		{"alternatives", `{"apiGroups":["","batch"],"apiVersions":["v1","v2"],"resources":["pods","jobs"]}`, true},
		{"missing groups", `{"apiVersions":["v1"],"resources":["pods"]}`, false},
		{"missing versions", `{"apiGroups":["batch"],"resources":["jobs"]}`, false},
		{"missing resources", `{"apiGroups":["batch"],"apiVersions":["v1"]}`, false},
		{"empty version", `{"apiGroups":["batch"],"apiVersions":["v1",""],"resources":["jobs"]}`, false},
		{"empty resource", `{"apiGroups":["batch"],"apiVersions":["v1"],"resources":["jobs",""]}`, false},
		{"group wildcard", `{"apiGroups":["*.example.com"],"apiVersions":["v1"],"resources":["jobs"]}`, false},
		{"version wildcard", `{"apiGroups":["batch"],"apiVersions":["v*"],"resources":["jobs"]}`, false},
		{"resource wildcard", `{"apiGroups":["batch"],"apiVersions":["v1"],"resources":["job*"]}`, false},
		{"double wildcard", `{"apiGroups":["batch"],"apiVersions":["v1"],"resources":["**"]}`, false},
		{"subresource", `{"apiGroups":["batch"],"apiVersions":["v1"],"resources":["jobs/status"]}`, false},
		{"wildcard subresource", `{"apiGroups":["batch"],"apiVersions":["v1"],"resources":["jobs/*"]}`, false},
		{"all subresources", `{"apiGroups":["*"],"apiVersions":["*"],"resources":["*/*"]}`, false},
		{"whitespace", `{"apiGroups":["batch"],"apiVersions":[" "],"resources":["jobs"]}`, false},
	} {
		for _, field := range []string{"resourceRules", "excludeResourceRules"} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(path, []byte(`{"`+field+`":[`+tc.rule+`]}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := loadOptions(path, "", "prometheus", "all"); (err == nil) != tc.wantValid {
					t.Fatalf("valid = %v, want %v; err = %v", err == nil, tc.wantValid, err)
				}
			})
		}
	}
}
