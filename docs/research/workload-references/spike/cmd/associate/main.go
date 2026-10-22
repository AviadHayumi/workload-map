// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	association "karta-reference-spike"
)

func main() {
	inputPath := flag.String("input", "", "captured Pods and target sets as JSON")
	rulesPath := flag.String("rules", "", "association rules as a JSON array")
	flag.Parse()
	if err := run(*inputPath, *rulesPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(inputPath, rulesPath string) error {
	input, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	var capture association.Capture
	if err := json.Unmarshal(input, &capture); err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	rulesData, err := os.ReadFile(rulesPath)
	if err != nil {
		return err
	}
	var rules []association.Rule
	if err := json.Unmarshal(rulesData, &rules); err != nil {
		return fmt.Errorf("rules: %w", err)
	}
	results, err := association.Evaluate(capture, rules)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(results)
}
