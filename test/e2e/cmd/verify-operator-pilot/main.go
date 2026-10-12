// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/onsi/ginkgo/v2/reporters"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/workload-map/test/e2e/recorder"
)

const pilotKarta = "jobset-x-k8s-io-jobset-v1alpha2"

var pilotFlows = [...]string{"running", "completed", "failed", "resumed", "suspended"}

func main() {
	version := flag.String("expected-version", "", "JobSet version installed by the pilot, including v prefix")
	junit := flag.String("junit", "", "Ginkgo JUnit report from the recording run")
	root := flag.String("recorded-root", "recorded_data", "recorded fixture root")
	flag.Parse()
	if flag.NArg() != 0 || !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(*version) || *junit == "" {
		fmt.Fprintln(os.Stderr, "usage: verify-operator-pilot -expected-version vX.Y.Z -junit report.xml [-recorded-root recorded_data]")
		os.Exit(2)
	}
	if err := verifyReport(*junit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := verifyRecordings(*root, *version); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("verified %d passed JobSet flows and recordings for %s\n", len(pilotFlows), *version)
}

func verifyReport(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read JUnit report: %w", err)
	}
	var report reporters.JUnitTestSuites
	if err := xml.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("parse JUnit report: %w", err)
	}
	if len(report.TestSuites) != 1 || report.Errors != 0 || report.Failures != 0 {
		return fmt.Errorf("expected one successful Ginkgo suite in %s", path)
	}
	suite := report.TestSuites[0]
	if suite.Name != "Karta E2E Flows" || suite.Errors != 0 || suite.Failures != 0 || suite.Disabled != 0 ||
		report.Tests != len(suite.TestCases) || suite.Tests != len(suite.TestCases) || report.Disabled != suite.Skipped {
		return fmt.Errorf("unexpected suite identity, results, or counters in %s", path)
	}
	for name, want := range map[string]string{
		"SuiteSucceeded":            "true",
		"SuiteHasProgrammaticFocus": "false",
		"SpecialSuiteFailureReason": "",
		"DryRun":                    "false",
		"LabelFilter":               "jobset",
	} {
		if value := suite.Properties.WithName(name); value != want {
			return fmt.Errorf("JUnit property %s = %q, want %q", name, value, want)
		}
	}

	remaining := make(map[string]bool, len(pilotFlows))
	for _, flow := range pilotFlows {
		remaining["[It] JobSet "+flow+" [jobset]"] = true
	}
	skipped := 0
	for _, test := range suite.TestCases {
		if test.Classname != suite.Name || test.Error != nil || test.Failure != nil {
			return fmt.Errorf("unexpected or failed JUnit case %q", test.Name)
		}
		switch {
		case remaining[test.Name]:
			if test.Status != "passed" || test.Skipped != nil {
				return fmt.Errorf("JobSet case %q did not pass", test.Name)
			}
			delete(remaining, test.Name)
		case strings.HasPrefix(test.Name, "[It] JobSet "):
			return fmt.Errorf("unexpected or duplicate JobSet case %q", test.Name)
		case strings.HasPrefix(test.Name, "[It] "):
			// Ginkgo includes label-filtered specs in JUnit as skipped cases.
			if test.Status != "skipped" || test.Skipped == nil || test.Skipped.Message != "skipped" {
				return fmt.Errorf("unexpected execution outside JobSet: %q", test.Name)
			}
			skipped++
		default:
			// BeforeSuite and suite cleanup are separate JUnit cases.
			if test.Status != "passed" || test.Skipped != nil {
				return fmt.Errorf("suite setup or cleanup %q did not pass", test.Name)
			}
		}
	}
	if len(remaining) != 0 {
		return fmt.Errorf("JUnit report is missing %d successful JobSet flows", len(remaining))
	}
	if skipped != suite.Skipped {
		return fmt.Errorf("JUnit skipped count is %d, want %d", suite.Skipped, skipped)
	}
	return nil
}

func verifyRecordings(root, version string) error {
	versionDir := filepath.Join(root, "jobset", version)
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		return fmt.Errorf("read candidate recordings: %w", err)
	}
	if len(entries) != 1 || entries[0].Name() != pilotKarta || !entries[0].IsDir() {
		return fmt.Errorf("expected only %s under %s", pilotKarta, versionDir)
	}
	dir := filepath.Join(versionDir, pilotKarta)
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read flow recordings: %w", err)
	}
	if len(files) != len(pilotFlows) {
		return fmt.Errorf("expected exactly %d flow recordings under %s, found %d", len(pilotFlows), dir, len(files))
	}
	for _, file := range files {
		if !file.Type().IsRegular() {
			return fmt.Errorf("expected a regular recording file: %s", file.Name())
		}
	}
	for _, flow := range pilotFlows {
		path := filepath.Join(dir, flow+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read flow recording: %w", err)
		}
		var recording recorder.Recording
		if err := yaml.UnmarshalStrict(data, &recording); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if recording.SchemaVersion != 1 || recording.Operator != "jobset" || recording.Version != version ||
			recording.Flow != flow || recording.KartaName != pilotKarta || recording.KartaFile != "docs/catalog/"+pilotKarta+".yaml" {
			return fmt.Errorf("unexpected recording metadata in %s", path)
		}
		if !recording.Result.Succeeded || len(recording.Result.Failures) != 0 {
			return fmt.Errorf("recorded flow did not succeed: %s", path)
		}
		states := 0
		for _, event := range recording.Events {
			if event.Kind == recorder.EventState {
				if event.State == "" || event.Object["kind"] != "JobSet" || event.Object["apiVersion"] != "jobset.x-k8s.io/v1alpha2" {
					return fmt.Errorf("invalid STATE event in %s", path)
				}
				states++
			}
		}
		if states == 0 {
			return fmt.Errorf("no STATE events in %s", path)
		}
	}
	return nil
}
