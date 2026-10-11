// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2/reporters"
	"github.com/onsi/ginkgo/v2/types"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/workload-map/test/e2e/recorder"
)

func TestVerifyReport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*types.Report)
	}{
		{name: "passed with suite setup and filtered operators"},
		{name: "dry run", change: func(r *types.Report) { r.SuiteConfig.DryRun = true }},
		{name: "wrong label", change: func(r *types.Report) { r.SuiteConfig.LabelFilter = "lws" }},
		{name: "suite failed", change: func(r *types.Report) { r.SuiteSucceeded = false }},
		{name: "focused suite", change: func(r *types.Report) { r.SuiteHasProgrammaticFocus = true }},
		{name: "missing flow", change: func(r *types.Report) { r.SpecReports = r.SpecReports[:len(r.SpecReports)-1] }},
		{name: "duplicate flow", change: func(r *types.Report) { r.SpecReports = append(r.SpecReports, r.SpecReports[2]) }},
		{name: "selected flow skipped", change: func(r *types.Report) { r.SpecReports[2].State = types.SpecStateSkipped }},
		{name: "selected flow pending", change: func(r *types.Report) { r.SpecReports[2].State = types.SpecStatePending }},
		{name: "selected flow failed", change: func(r *types.Report) { r.SpecReports[2].State = types.SpecStateFailed }},
		{name: "selected flow panicked", change: func(r *types.Report) { r.SpecReports[2].State = types.SpecStatePanicked }},
		{name: "setup failed", change: func(r *types.Report) { r.SpecReports[0].State = types.SpecStateFailed }},
		{name: "another operator executed", change: func(r *types.Report) { r.SpecReports[1].State = types.SpecStatePassed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := types.Report{
				SuiteDescription: "Karta E2E Flows",
				SuiteSucceeded:   true,
				SuiteConfig:      types.SuiteConfig{LabelFilter: "jobset"},
				SpecReports: types.SpecReports{
					{LeafNodeType: types.NodeTypeBeforeSuite, State: types.SpecStatePassed},
					{LeafNodeType: types.NodeTypeIt, LeafNodeText: "running", ContainerHierarchyTexts: []string{"LeaderWorkerSet"}, ContainerHierarchyLabels: [][]string{{"lws"}}, State: types.SpecStateSkipped},
				},
			}
			for _, flow := range pilotFlows {
				report.SpecReports = append(report.SpecReports, types.SpecReport{
					LeafNodeType:             types.NodeTypeIt,
					LeafNodeText:             flow,
					ContainerHierarchyTexts:  []string{"JobSet"},
					ContainerHierarchyLabels: [][]string{{"jobset"}},
					State:                    types.SpecStatePassed,
				})
			}
			if tc.change != nil {
				tc.change(&report)
			}
			path := filepath.Join(t.TempDir(), "junit.xml")
			if err := reporters.GenerateJUnitReport(report, path); err != nil {
				t.Fatal(err)
			}
			err := verifyReport(path)
			if (err != nil) != (tc.change != nil) {
				t.Fatalf("verifyReport() error = %v, want rejection %t", err, tc.change != nil)
			}
		})
	}
}

func TestVerifyRecordings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*recorder.Recording)
	}{
		{name: "five successful versioned recordings"},
		{name: "Kubernetes fallback version", change: func(r *recorder.Recording) { r.Version = "v1.34.0" }},
		{name: "wrong operator", change: func(r *recorder.Recording) { r.Operator = "lws" }},
		{name: "wrong flow", change: func(r *recorder.Recording) { r.Flow = "completed" }},
		{name: "wrong Karta", change: func(r *recorder.Recording) { r.KartaName = "another-karta" }},
		{name: "wrong catalog path", change: func(r *recorder.Recording) { r.KartaFile = "docs/catalog/another-karta.yaml" }},
		{name: "failed recording", change: func(r *recorder.Recording) { r.Result.Succeeded = false }},
		{name: "failure alongside success", change: func(r *recorder.Recording) { r.Result.Failures = []string{"cleanup failed"} }},
		{name: "empty recording", change: func(r *recorder.Recording) { r.Events = nil }},
		{name: "actions only", change: func(r *recorder.Recording) { r.Events[0].Kind = recorder.EventAction }},
		{name: "missing state", change: func(r *recorder.Recording) { r.Events[0].State = "" }},
		{name: "missing object", change: func(r *recorder.Recording) { r.Events[0].Object = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writePilotRecordings(t, root, tc.change)
			err := verifyRecordings(root, "v0.12.0")
			if (err != nil) != (tc.change != nil) {
				t.Fatalf("verifyRecordings() error = %v, want rejection %t", err, tc.change != nil)
			}
		})
	}
}

func TestVerifyRecordingsRequiresExactFiles(t *testing.T) {
	for _, change := range []string{"missing", "extra", "directory", "symlink", "another karta"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			writePilotRecordings(t, root, nil)
			dir := filepath.Join(root, "jobset", "v0.12.0", pilotKarta)
			switch change {
			case "missing":
				if err := os.Remove(filepath.Join(dir, "running.yaml")); err != nil {
					t.Fatal(err)
				}
			case "extra":
				if err := os.WriteFile(filepath.Join(dir, "extra.yaml"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			case "directory", "symlink":
				path := filepath.Join(dir, "running.yaml")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if change == "directory" {
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(filepath.Join(dir, "completed.yaml"), path); err != nil {
					t.Fatal(err)
				}
			case "another karta":
				if err := os.Mkdir(filepath.Join(root, "jobset", "v0.12.0", "another-karta"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyRecordings(root, "v0.12.0"); err == nil {
				t.Fatal("accepted an incomplete or unexpected recording set")
			}
		})
	}
}

func writePilotRecordings(t *testing.T, root string, change func(*recorder.Recording)) {
	t.Helper()
	dir := filepath.Join(root, "jobset", "v0.12.0", pilotKarta)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, flow := range pilotFlows {
		r := recorder.Recording{
			SchemaVersion: 1,
			Operator:      "jobset",
			Version:       "v0.12.0",
			Flow:          flow,
			KartaName:     pilotKarta,
			KartaFile:     "docs/catalog/" + pilotKarta + ".yaml",
			Result:        recorder.Result{Succeeded: true},
			Events: []recorder.Event{{
				Kind:   recorder.EventState,
				State:  "Running",
				Object: map[string]any{"apiVersion": "jobset.x-k8s.io/v1alpha2", "kind": "JobSet"},
			}},
		}
		if flow == "running" && change != nil {
			change(&r)
		}
		data, err := yaml.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, flow+".yaml"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
