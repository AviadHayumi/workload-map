// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCommandProcess runs the actual command in a child so a startup regression
// cannot leave the test suite waiting for a signal.
func TestCommandProcess(t *testing.T) {
	if os.Getenv("KARTA_TEST_COMMAND_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"karta-metrics-exporter"}, os.Args[i+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing command arguments")
}

func TestStartupRejectsInvalidExpressions(t *testing.T) {
	for _, tc := range []struct{ name, expression, limits, want string }{
		{"syntax", `object[`, "", "compiling label expression"},
		{"non string", `1 + 1`, "", "must produce a string"},
		{"expression limit", `object.kind`, "labelLimits:\n  maxExpressionBytes: 4\n", "exceeds the maximum length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			contents := "labelRules:\n- name: team\n  metrics: [karta_workload_status]\n  sources: [workload]\n  expression: '" + tc.expression + "'\n" + tc.limits
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandProcess$", "--", "--config", path)
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "KARTA_EXPORTER_") && !strings.HasPrefix(entry, "KARTA_TEST_COMMAND_PROCESS=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "KARTA_TEST_COMMAND_PROCESS=1")
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "command waited for a signal instead of rejecting configuration")
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			require.Equal(t, 1, exit.ExitCode())
			require.Contains(t, string(output), tc.want)
		})
	}
}
