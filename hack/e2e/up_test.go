// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type provisionResult struct {
	output   string
	events   []string
	versions []string
	summary  string
	tools    string
	exitCode int
}

func TestInfrastructureInstallPlan(t *testing.T) {
	all := "lws jobset kuberay kubeflow knative kserve milvus grove dynamo nim agent-sandbox"
	cases := []struct {
		name      string
		workloads []string
		env       []string
		infra     string
		plan      string
		wantError string
	}{
		{name: "default", infra: "cert-manager fake-gpu-operator kai-scheduler", plan: all},
		{name: "all", workloads: []string{"all"}, infra: "cert-manager fake-gpu-operator kai-scheduler", plan: all},
		{name: "none", workloads: []string{"none"}, infra: "fake-gpu-operator", plan: "(none)"},
		{name: "jobset", workloads: []string{"jobset"}, infra: "fake-gpu-operator", plan: "jobset"},
		{name: "grove", workloads: []string{"grove"}, infra: "fake-gpu-operator kai-scheduler", plan: "grove"},
		{name: "dynamo dependency", workloads: []string{"dynamo"}, infra: "fake-gpu-operator kai-scheduler", plan: "grove dynamo"},
		{name: "kserve dependency", workloads: []string{"kserve"}, infra: "cert-manager fake-gpu-operator", plan: "knative kserve"},
		{name: "deduplicated dependencies", workloads: []string{"dynamo", "grove", "dynamo"}, infra: "fake-gpu-operator kai-scheduler", plan: "grove dynamo"},
		{name: "forced cert manager", workloads: []string{"none"}, env: []string{"CERT_MANAGER=true"}, infra: "cert-manager fake-gpu-operator", plan: "(none)"},
		{name: "webhook cert manager", workloads: []string{"none"}, env: []string{"KARTA_WEBHOOK_MODE=cert-manager"}, infra: "cert-manager fake-gpu-operator", plan: "(none)"},
		{name: "disabled webhook", workloads: []string{"none"}, env: []string{"KARTA_WEBHOOK_MODE=disabled"}, infra: "fake-gpu-operator", plan: "(none)"},
		{name: "disabled cert manager", workloads: []string{"grove"}, env: []string{"CERT_MANAGER=false"}, infra: "fake-gpu-operator kai-scheduler", plan: "grove"},
		{name: "kserve requires cert manager", workloads: []string{"kserve"}, env: []string{"CERT_MANAGER=false"}, wantError: "cert-manager is required by: operator kserve"},
		{name: "webhook requires cert manager", workloads: []string{"none"}, env: []string{"CERT_MANAGER=false", "KARTA_WEBHOOK_MODE=cert-manager"}, wantError: "cert-manager is required by: KARTA_WEBHOOK_MODE=cert-manager"},
		{name: "invalid cert manager mode", workloads: []string{"none"}, env: []string{"CERT_MANAGER=maybe"}, wantError: "unknown CERT_MANAGER"},
		{name: "invalid webhook mode", workloads: []string{"none"}, env: []string{"KARTA_WEBHOOK_MODE=maybe"}, wantError: "unknown KARTA_WEBHOOK_MODE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := runProvision(t, append([]string{"--list"}, tc.workloads...), tc.env...)
			if tc.wantError != "" {
				if result.exitCode != 2 || !strings.Contains(result.output, tc.wantError) {
					t.Fatalf("exit %d, want 2 and %q:\n%s", result.exitCode, tc.wantError, result.output)
				}
			} else {
				if result.exitCode != 0 {
					t.Fatalf("plan exited %d:\n%s", result.exitCode, result.output)
				}
				for _, want := range []string{"infrastructure: " + tc.infra + "\n", "workloads: " + tc.plan + "\n"} {
					if !strings.Contains(result.output, want) {
						t.Errorf("plan does not contain %q:\n%s", want, result.output)
					}
				}
			}
			if len(result.events) != 0 || result.tools != "" {
				t.Fatalf("planning invoked provisioning: events %v, tools %q", result.events, result.tools)
			}
			if !slices.Equal(result.versions, []string{"stale=old"}) {
				t.Errorf("planning changed installed versions: %v", result.versions)
			}
		})
	}
}

func TestInfrastructureProvisioning(t *testing.T) {
	cases := []struct {
		name       string
		workload   string
		env        []string
		components []string
		versions   []string
		failStep   string
	}{
		{
			name: "base only", workload: "none", components: []string{"infra/fake-gpu-operator", "karta-operator"},
			versions: []string{"fake-gpu-operator=9.2.0"},
		},
		{
			name: "all infra before grove and dynamo", workload: "dynamo", env: []string{"CERT_MANAGER=true"},
			components: []string{"infra/cert-manager", "infra/fake-gpu-operator", "infra/kai-scheduler", "operators/grove", "operators/dynamo", "karta-operator"},
			versions:   []string{"cert-manager=v9.1.0", "fake-gpu-operator=9.2.0", "kai-scheduler=v9.3.0", "grove=v9.4.0", "dynamo=9.5.0"},
		},
		{
			name: "cert manager before kserve", workload: "kserve",
			components: []string{"infra/cert-manager", "infra/fake-gpu-operator", "operators/knative", "operators/kserve", "karta-operator"},
			versions:   []string{"cert-manager=v9.1.0", "fake-gpu-operator=9.2.0", "knative=knative-v9.6.0", "kserve=v9.7.0"},
		},
		{
			name: "install failure skips verify and dependents", workload: "dynamo",
			components: []string{"infra/fake-gpu-operator", "infra/kai-scheduler"}, failStep: "infra/kai-scheduler/install",
			versions: []string{"fake-gpu-operator=9.2.0"},
		},
		{
			name: "verify failure skips dependents", workload: "dynamo",
			components: []string{"infra/fake-gpu-operator", "infra/kai-scheduler"}, failStep: "infra/kai-scheduler/verify",
			versions: []string{"fake-gpu-operator=9.2.0"},
		},
		{
			name: "first infra failure clears stale versions", workload: "none", env: []string{"CERT_MANAGER=true"},
			components: []string{"infra/cert-manager"}, failStep: "infra/cert-manager/install",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := runProvision(t, []string{tc.workload}, append(tc.env, "E2E_TEST_FAIL_STEP="+tc.failStep)...)
			wantExit := 0
			if tc.failStep != "" {
				wantExit = 1
				if !strings.Contains(result.summary, "| :x: |") {
					t.Errorf("failure missing from summary:\n%s", result.summary)
				}
			}
			if result.exitCode != wantExit {
				t.Fatalf("provisioning exited %d, want %d:\n%s", result.exitCode, wantExit, result.output)
			}
			var wantEvents []string
			for _, component := range tc.components {
				wantEvents = append(wantEvents, component+"/install")
				if tc.failStep == component+"/install" {
					break
				}
				wantEvents = append(wantEvents, component+"/verify")
			}
			if !slices.Equal(result.events, wantEvents) {
				t.Errorf("component order = %v, want %v", result.events, wantEvents)
			}
			if !slices.Equal(result.versions, tc.versions) {
				t.Errorf("installed versions = %v, want %v", result.versions, tc.versions)
			}
			for _, version := range tc.versions {
				name, pin, _ := strings.Cut(version, "=")
				if !strings.Contains(result.summary, "| :white_check_mark: | "+name+" | "+pin+" |") {
					t.Errorf("successful component %s missing from summary:\n%s", version, result.summary)
				}
			}
		})
	}
}

// Run the real orchestrator with child scripts and cluster tools replaced. The
// fixture also isolates the installed-version file from developer clusters.
func runProvision(t *testing.T, args []string, env ...string) provisionResult {
	t.Helper()
	root := t.TempDir()
	e2eDir := filepath.Join(root, "hack", "e2e")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"up.sh", "global.env", "operators/_common.sh"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(e2eDir, path), string(content))
	}
	installers, err := filepath.Glob("operators/*/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	components := []string{"infra/cert-manager", "infra/fake-gpu-operator", "infra/kai-scheduler", "karta-operator"}
	for _, installer := range installers {
		components = append(components, filepath.Dir(installer))
	}
	for _, component := range components {
		for _, phase := range []string{"install", "verify"} {
			step := filepath.ToSlash(component) + "/" + phase
			write(filepath.Join(e2eDir, component, phase+".sh"), fmt.Sprintf(`#!/bin/bash
set -eu
printf '%%s\n' '%s' >> "$E2E_TEST_EVENTS"
if [ "${E2E_TEST_FAIL_STEP:-}" = '%s' ]; then exit 7; fi
`, step, step))
		}
	}
	bin := filepath.Join(root, "bin")
	for _, tool := range []string{"docker", "kind", "kubectl", "helm", "curl", "tar"} {
		write(filepath.Join(bin, tool), `#!/bin/bash
set -eu
printf '%s\n' "${0##*/} $*" >> "$E2E_TEST_TOOLS"
case "${0##*/} $*" in
  'kubectl get namespace cert-manager') exit 1 ;;
  curl* | tar*) exit 99 ;;
esac
`)
	}
	versions := filepath.Join(e2eDir, "operators", ".installed-versions-karta-e2e")
	write(versions, "stale=old\n")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", append([]string{filepath.Join(e2eDir, "up.sh")}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CLUSTER_NAME=karta-e2e", "KUBECONFIG="+filepath.Join(root, "kubeconfig"), "GO_VERSION=1.26.8",
		"KARTA_WEBHOOK_MODE=auto", "CERT_MANAGER=auto", "GITHUB_ACTIONS=", "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"),
		"CERT_MANAGER_VERSION=v9.1.0", "FAKE_GPU_VERSION=9.2.0", "KAI_SCHEDULER_VERSION=v9.3.0", "GROVE_VERSION=v9.4.0",
		"DYNAMO_VERSION=9.5.0", "KNATIVE_VERSION=knative-v9.6.0", "KSERVE_VERSION=v9.7.0",
		"E2E_TEST_EVENTS="+filepath.Join(root, "events"), "E2E_TEST_TOOLS="+filepath.Join(root, "tools"), "E2E_TEST_FAIL_STEP=",
	)
	cmd.Env = append(cmd.Env, env...)
	output, err := cmd.CombinedOutput()
	result := provisionResult{output: string(output)}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		result.exitCode = exit.ExitCode()
	}
	read := func(path string) string {
		t.Helper()
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(content))
	}
	if events := read(filepath.Join(root, "events")); events != "" {
		result.events = strings.Split(events, "\n")
	}
	if installed := read(versions); installed != "" {
		result.versions = strings.Split(installed, "\n")
	}
	result.summary = read(filepath.Join(root, "summary"))
	result.tools = read(filepath.Join(root, "tools"))
	return result
}
