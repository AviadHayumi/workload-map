#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Offline regression tests for operator update discovery and reporting."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("check_updates.py")
SPEC = importlib.util.spec_from_file_location("check_updates", SCRIPT)
updates = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(updates)


class UpdateCheckTests(unittest.TestCase):
    def run_check(self, tags, pinned="1.9.0", prefix="v", channel="stable",
                  include_prereleases=False, name="example", repository="owner/operator"):
        output = io.StringIO()
        with mock.patch.object(updates, "remote_tags", return_value=tags), contextlib.redirect_stdout(output):
            changed = updates.check_operator(name, pinned, repository, prefix, channel,
                                             "", include_prereleases)
        return changed, output.getvalue()

    def run_main(self, operators, tags=None, error=None, options=()):
        argv = []
        for operator in operators:
            argv.extend(["--operator", *operator])
        argv.extend(options)
        stdout = io.StringIO()
        stderr = io.StringIO()
        with mock.patch.object(updates, "remote_tags", return_value=tags, side_effect=error), \
                contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            status = updates.main(argv)
        return status, stdout.getvalue(), stderr.getvalue()

    def test_numeric_versions_and_new_tag_count(self):
        changed, output = self.run_check(["v1.9.0", "v1.9.2", "v1.10.0", "v1.8.99"])
        self.assertTrue(changed)
        self.assertEqual(output.splitlines()[0].split(), ["NEW", "example", "1.9.0", "v1.10.0"])
        self.assertIn("2 newer tag(s)", output)
        self.assertIn("/compare/v1.9.0...v1.10.0", output)

    def test_tag_input_order_does_not_determine_latest(self):
        _, output = self.run_check(["v2.0.0", "v1.10.0", "v1.9.1"])
        self.assertEqual(output.splitlines()[0].split()[-1], "v2.0.0")

    def test_stable_channel_excludes_prereleases(self):
        changed, output = self.run_check(["v1.9.0", "v1.10.0-rc.1", "v9.0.0-dev"])
        self.assertFalse(changed)
        self.assertTrue(output.startswith("CURRENT "))
        self.assertNotIn("/compare/", output)

    def test_include_prereleases_orders_numeric_identifiers(self):
        changed, output = self.run_check(["v1.10.0-rc.2", "v1.10.0-rc.10", "v1.9.0"],
                                         include_prereleases=True)
        self.assertTrue(changed)
        self.assertEqual(output.splitlines()[0].split()[-1], "v1.10.0-rc.10")

    def test_undotted_release_candidates_follow_upstream_numeric_order(self):
        tags = ["v1.6.0-rc9", "v1.6.0-rc10"]
        changed, output = self.run_check(tags, pinned="v1.6.0-rc9", include_prereleases=True)
        self.assertTrue(changed)
        self.assertEqual(output.splitlines()[0].split()[-1], "v1.6.0-rc10")
        self.assertIn("1 newer tag(s)", output)
        _, output = self.run_check(tags + ["v1.6.0"], pinned="v1.6.0-rc9", include_prereleases=True)
        self.assertEqual(output.splitlines()[0].split()[-1], "v1.6.0")
        self.assertIn("2 newer tag(s)", output)

    def test_grove_channel_includes_prereleases_by_default(self):
        changed, output = self.run_check(["v0.1.0-alpha.1", "v0.1.0-alpha.2"],
                                         pinned="v0.1.0-alpha.1", channel="alpha", name="grove")
        self.assertTrue(changed)
        self.assertIn("v0.1.0-alpha.2", output)

    def test_grove_alpha_channel_excludes_release_candidates(self):
        changed, output = self.run_check(["v0.1.0-alpha.10", "v0.1.0-alpha.11-rc1"],
                                         pinned="v0.1.0-alpha.10-rc1", channel="alpha")
        self.assertTrue(changed)
        self.assertEqual(output.splitlines()[0].split()[-1], "v0.1.0-alpha.10")

    def test_grove_promotion_order_with_release_candidates_enabled(self):
        tags = ["v0.1.0-alpha.9", "v0.1.0-alpha.10-rc2", "v0.1.0-alpha.10-rc10",
                "v0.1.0-alpha.10"]
        changed, output = self.run_check(tags, pinned="v0.1.0-alpha.10-rc2",
                                         channel="alpha", include_prereleases=True)
        self.assertTrue(changed)
        self.assertEqual(output.splitlines()[0].split()[-1], "v0.1.0-alpha.10")
        self.assertIn("2 newer tag(s)", output)
        _, output = self.run_check(tags[:-1], pinned="v0.1.0-alpha.10-rc2",
                                   channel="alpha", include_prereleases=True)
        self.assertEqual(output.splitlines()[0].split()[-1], "v0.1.0-alpha.10-rc10")

    def test_missing_pin_does_not_generate_broken_compare_link(self):
        changed, output = self.run_check(["v1.10.0"])
        self.assertTrue(changed)
        self.assertNotIn("/compare/", output)
        self.assertIn("Pinned tag v1.9.0 was not found", output)

    def test_excluded_retracted_tags_do_not_trigger_updates(self):
        operator = ("kai-scheduler", "v0.15.2", "kai-scheduler/KAI-Scheduler", "v", "stable", "")
        options = ["--fail-on-update", "--exclude-tag", "v0.20.0", "--exclude-tag", "v0.20.1"]
        status, output, errors = self.run_main([operator], tags=["v0.15.2", "v0.20.0", "v0.20.1"],
                                               options=options)
        self.assertEqual(status, 0)
        self.assertTrue("CURRENT" in output)
        self.assertEqual(errors, "")

    def test_final_release_sorts_after_same_version_prereleases(self):
        changed, output = self.run_check(["v1.9.0-rc.9", "v1.9.0"],
                                         pinned="v1.9.0-rc.9", include_prereleases=True)
        self.assertTrue(changed)
        self.assertEqual(output.splitlines()[0].split()[-1], "v1.9.0")

    def test_component_tag_namespaces(self):
        cases = [
            ("milvus", "1.3.7", "milvus-operator-", ["v9.0.0", "config-tool-v9.0.0",
              "milvus-operator-1.3.7", "milvus-operator-1.3.11"], "milvus-operator-1.3.11"),
            ("kuberay", "1.4.0", "kuberay-operator-", ["ray-operator-9.0.0", "v9.0.0",
              "kuberay-operator-1.4.0", "kuberay-operator-1.5.0"], "kuberay-operator-1.5.0"),
            ("knative", "knative-v1.15.2", "knative-v", ["v9.0.0", "v0.50.0",
              "knative-v1.15.2", "knative-v1.23.0"], "knative-v1.23.0"),
        ]
        for name, pinned, prefix, tags, expected in cases:
            with self.subTest(name=name):
                changed, output = self.run_check(tags, pinned=pinned, prefix=prefix, name=name)
                self.assertTrue(changed)
                self.assertEqual(output.splitlines()[0].split()[-1], expected)

    def test_installer_pin_prefixes_generate_correct_compare_refs(self):
        cases = [("1.2.3", "v", "v1.2.3"), ("v1.2.3", "v", "v1.2.3"),
                 ("knative-v1.2.3", "knative-v", "knative-v1.2.3"),
                 ("1.2.3", "milvus-operator-", "milvus-operator-1.2.3"),
                 ("milvus-operator-1.2.3", "milvus-operator-", "milvus-operator-1.2.3"),
                 ("1.2.3", "", "1.2.3")]
        for pinned, prefix, expected in cases:
            with self.subTest(pinned=pinned, prefix=prefix):
                _, output = self.run_check([expected, prefix + "1.3.0"], pinned=pinned, prefix=prefix)
                self.assertIn("/compare/" + expected + "...", output)

    def test_build_metadata_does_not_create_update(self):
        changed, output = self.run_check(["v1.9.0+build.99"], pinned="1.9.0+build.1")
        self.assertFalse(changed)
        self.assertTrue(output.startswith("CURRENT "))

    def test_build_metadata_is_encoded_in_compare_link(self):
        _, output = self.run_check(["v1.9.0+build.1", "v1.10.0+build.2"], pinned="v1.9.0+build.1")
        self.assertIn("/compare/v1.9.0%2Bbuild.1...v1.10.0%2Bbuild.2", output)

    def test_newer_local_pin_is_reported_as_ahead(self):
        changed, output = self.run_check(["v1.8.9"])
        self.assertFalse(changed)
        self.assertTrue(output.startswith("AHEAD "))

    def test_invalid_tags_are_ignored(self):
        changed, output = self.run_check(["v1.9.0", "v2.0", "v02.0.0", "v2.0.0-01",
                                         "v2.0.0^{}", "v2.0.0/extra", "nightly"],
                                         include_prereleases=True)
        self.assertFalse(changed)
        self.assertEqual(output.splitlines()[0].split()[-1], "v1.9.0")

    def test_invalid_pin_fails_before_remote_lookup(self):
        for pinned in ("latest", "v1.2", "1.02.3", "1.2.3-01"):
            with self.subTest(pinned=pinned), mock.patch.object(updates, "remote_tags") as lookup:
                with self.assertRaisesRegex(ValueError, "invalid"):
                    updates.check_operator("example", pinned, "owner/operator", "v", "stable", "", False)
                lookup.assert_not_called()

    def test_empty_or_nonmatching_tag_sets_fail(self):
        for tags in ([], ["other-2.0.0"], ["v2.0.0-rc.1"]):
            with self.subTest(tags=tags):
                with self.assertRaisesRegex(ValueError, "no matching stable tags"):
                    self.run_check(tags)

    def test_invalid_repository_and_channel_fail_before_lookup(self):
        for repository, channel in (("https://github.com/owner/repo", "stable"),
                                    ("owner/repo/extra", "stable"), ("owner/repo", "unknown")):
            with self.subTest(repository=repository, channel=channel), \
                    mock.patch.object(updates, "remote_tags") as lookup:
                with self.assertRaises(ValueError):
                    updates.check_operator("example", "1.0.0", repository, "v", channel, "", False)
                lookup.assert_not_called()

    def test_fail_on_update_is_opt_in(self):
        operator = ("example", "1.9.0", "owner/operator", "v", "stable", "")
        status, output, errors = self.run_main([operator], tags=["v1.10.0"])
        self.assertEqual(status, 0)
        self.assertIn("NEW", output)
        self.assertEqual(errors, "")
        status, _, _ = self.run_main([operator], tags=["v1.10.0"], options=["--fail-on-update"])
        self.assertEqual(status, 2)

    def test_current_pin_succeeds_with_fail_on_update(self):
        operator = ("example", "1.9.0", "owner/operator", "v", "stable", "")
        status, _, _ = self.run_main([operator], tags=["v1.9.0"], options=["--fail-on-update"])
        self.assertEqual(status, 0)

    def test_errors_take_precedence_and_do_not_stop_other_checks(self):
        operators = [("one", "1.0.0", "owner/one", "v", "stable", ""),
                     ("two", "1.0.0", "owner/two", "v", "stable", "")]
        for errors in ([ValueError("lookup failed"), ["v2.0.0"]],
                       [["v2.0.0"], ValueError("lookup failed")]):
            with self.subTest(errors=errors):
                status, output, stderr = self.run_main(operators, error=errors,
                                                       options=["--fail-on-update"])
                self.assertEqual(status, 1)
                self.assertIn("NEW", output)
                self.assertIn("ERROR", output)
                self.assertIn("lookup failed", stderr)

    def test_missing_git_and_timeout_are_reported_as_errors(self):
        operator = ("example", "1.9.0", "owner/operator", "v", "stable", "")
        for error in (FileNotFoundError("git missing"), subprocess.TimeoutExpired("git", 60)):
            with self.subTest(error=error):
                status, output, stderr = self.run_main([operator], error=error)
                self.assertEqual(status, 1)
                self.assertIn("ERROR", output)
                self.assertTrue(stderr)

    def test_no_header_option(self):
        operator = ("example", "1.9.0", "owner/operator", "v", "stable", "")
        status, output, _ = self.run_main([operator], tags=["v1.9.0"], options=["--no-header"])
        self.assertEqual(status, 0)
        self.assertTrue(output.startswith("CURRENT "))


class RemoteTagTests(unittest.TestCase):
    def test_remote_query_is_noninteractive_and_extracts_tags(self):
        result = subprocess.CompletedProcess([], 0, "a" * 40 + "\trefs/tags/v1.2.3\n" +
                                             "b" * 40 + "\trefs/tags/chart-1.3.0\n", "")
        with mock.patch.object(updates.subprocess, "run", return_value=result) as run:
            self.assertEqual(updates.remote_tags("owner/repo"), ["v1.2.3", "chart-1.3.0"])
        args, kwargs = run.call_args
        self.assertIn("--refs", args[0])
        self.assertIn("--tags", args[0])
        self.assertEqual(args[0][-1], "https://github.com/owner/repo.git")
        self.assertEqual(kwargs["env"]["GIT_TERMINAL_PROMPT"], "0")
        self.assertLessEqual(kwargs["timeout"], 60)

    def test_failed_remote_query_does_not_expose_git_stderr(self):
        result = subprocess.CompletedProcess([], 128, "", "sensitive diagnostic")
        with mock.patch.object(updates.subprocess, "run", return_value=result):
            with self.assertRaisesRegex(ValueError, "cannot read tags") as failure:
                updates.remote_tags("owner/repo")
        self.assertNotIn("sensitive diagnostic", str(failure.exception))

    def test_remote_timeout_is_preserved_for_reporting(self):
        with mock.patch.object(updates.subprocess, "run", side_effect=subprocess.TimeoutExpired("git", 60)):
            with self.assertRaises(subprocess.TimeoutExpired):
                updates.remote_tags("owner/repo")


class WrapperTests(unittest.TestCase):
    """Exercise the actual shell entry points with an offline git executable."""

    def run_wrapper(self, path="check-updates.sh", args=(), tags=None, pins=None, make=False):
        if tags is None:
            tags = {"kubernetes-sigs/jobset": ["v1.0.0", "v1.1.0"]}
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            log = directory / "calls.jsonl"
            git = directory / "git"
            git.write_text(
                "#!" + sys.executable + "\n"
                "import json, os, sys\n"
                "if 'ls-remote' not in sys.argv: sys.exit(0)\n"
                "with open(os.environ['UPDATE_TEST_LOG'], 'a') as log:\n"
                "    log.write(json.dumps(sys.argv[1:]) + '\\n')\n"
                "repo = sys.argv[-1].removeprefix('https://github.com/').removesuffix('.git')\n"
                "tags = json.loads(os.environ['UPDATE_TEST_TAGS']).get(repo)\n"
                "if tags is None: sys.exit(128)\n"
                "for tag in tags: print('a' * 40 + '\\trefs/tags/' + tag)\n"
            )
            git.chmod(0o755)
            for tool in ("kubectl", "helm", "kind", "docker"):
                forbidden = directory / tool
                forbidden.write_text("#!/bin/sh\necho 'unexpected cluster tool invocation' >&2\nexit 97\n")
                forbidden.chmod(0o755)
            env = {**os.environ, "PATH": str(directory) + os.pathsep + os.environ["PATH"],
                   "UPDATE_TEST_LOG": str(log), "UPDATE_TEST_TAGS": json.dumps(tags),
                   "PYTHONDONTWRITEBYTECODE": "1", "JOBSET_VERSION": "v1.0.0",
                   "KNATIVE_VERSION": "knative-v1.0.0", "KOURIER_VERSION": "knative-v1.0.0"}
            env.update(pins or {})
            if make:
                command = ["make", "-C", str(SCRIPT.parents[2]), "e2e-check-updates", *args]
            else:
                command = ["bash", str(SCRIPT.parent / path), *args]
            result = subprocess.run(command, cwd=directory, env=env, capture_output=True,
                                    text=True, timeout=30)
            calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
            return result, calls

    def test_single_operator_wrapper_honors_environment_pin(self):
        result, calls = self.run_wrapper("operators/jobset/check-updates.sh",
                                        args=["--fail-on-update"], pins={"JOBSET_VERSION": "v1.1.0"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("CURRENT", result.stdout)
        self.assertEqual(len(calls), 1)

    def test_aggregate_defaults_to_advisory_exit(self):
        result, calls = self.run_wrapper(args=["jobset"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("NEW", result.stdout)
        self.assertIn("New tags in operator groups: jobset", result.stdout)
        self.assertEqual(len(calls), 1)

    def test_aggregate_fail_on_update(self):
        result, _ = self.run_wrapper(args=["jobset", "--fail-on-update"])
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_aggregate_validates_all_selectors_before_network(self):
        for args in (["jobset", "unknown"], ["jobset", "--unsupported"]):
            with self.subTest(args=args):
                result, calls = self.run_wrapper(args=args)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(calls, [])
                self.assertIn("unknown", result.stderr)

    def test_standalone_usage_and_help_do_not_query_network(self):
        for flag, expected in (("--unsupported", 1), ("--help", 0)):
            with self.subTest(flag=flag):
                result, calls = self.run_wrapper("operators/jobset/check-updates.sh", args=[flag])
                self.assertEqual(result.returncode, expected)
                self.assertEqual(calls, [])

    def test_partial_group_failure_keeps_successful_update_in_summary(self):
        tags = {"knative/serving": None,
                "knative-extensions/net-kourier": ["knative-v1.1.0"]}
        result, calls = self.run_wrapper(args=["knative"], tags=tags)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertIn("New tags in operator groups: knative", result.stdout)
        self.assertIn("report is incomplete", result.stderr)

    def test_repeated_selectors_are_deduplicated(self):
        result, calls = self.run_wrapper(args=["jobset", "jobset"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)

    def test_multi_component_wrapper_continues_after_lookup_error(self):
        tags = {"knative/serving": None,
                "knative-extensions/net-kourier": ["knative-v1.0.0", "knative-v1.1.0"]}
        result, calls = self.run_wrapper("operators/knative/check-updates.sh",
                                        args=["--fail-on-update"], tags=tags)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertRegex(result.stdout, r"ERROR +knative")
        self.assertRegex(result.stdout, r"NEW +kourier")

    def test_aggregate_failure_takes_precedence_and_continues(self):
        tags = {"kubernetes-sigs/jobset": None,
                "knative/serving": ["knative-v1.0.0"],
                "knative-extensions/net-kourier": ["knative-v1.1.0"]}
        result, calls = self.run_wrapper(args=["jobset", "knative", "--fail-on-update"], tags=tags)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(len(calls), 3)
        self.assertRegex(result.stdout, r"NEW +kourier")
        self.assertIn("report is incomplete", result.stderr)

    def test_infra_selector_covers_all_infrastructure_and_kai_retractions(self):
        tags = {"cert-manager/cert-manager": ["v1.0.0"],
                "run-ai/fake-gpu-operator": ["v1.0.0"],
                "kai-scheduler/KAI-Scheduler": ["v0.15.2", "v0.20.0", "v0.20.1"]}
        pins = {"CERT_MANAGER_VERSION": "v1.0.0", "FAKE_GPU_VERSION": "1.0.0",
                "KAI_SCHEDULER_VERSION": "v0.15.2"}
        result, calls = self.run_wrapper(args=["infra", "--fail-on-update"], tags=tags, pins=pins)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 3)
        self.assertRegex(result.stdout, r"CURRENT +fake-gpu-operator")
        self.assertRegex(result.stdout, r"CURRENT +kai-scheduler")

    def test_all_wrappers_use_expected_release_repositories(self):
        repositories = {"cert-manager/cert-manager": "v", "run-ai/fake-gpu-operator": "v",
                        "kai-scheduler/KAI-Scheduler": "v", "kubernetes-sigs/lws": "v",
                        "kubernetes-sigs/jobset": "v", "ray-project/kuberay-helm": "kuberay-operator-",
                        "kubeflow/trainer": "v", "kubeflow/mpi-operator": "v",
                        "knative/serving": "knative-v", "knative-extensions/net-kourier": "knative-v",
                        "kserve/kserve": "v", "zilliztech/milvus-operator": "milvus-operator-",
                        "ai-dynamo/grove": "v", "ai-dynamo/dynamo": "v", "NVIDIA/k8s-nim-operator": "v"}
        tags = {repository: [prefix + "999.0.0"] for repository, prefix in repositories.items()}
        result, calls = self.run_wrapper(tags=tags)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 15)
        self.assertEqual(result.stdout.count("NEW "), 15)
        self.assertNotIn("unexpected cluster tool", result.stderr)

    def test_make_target_forwards_operator_and_flag_selection(self):
        tags = {"kubernetes-sigs/jobset": ["v1.0.0", "v1.1.0-rc.1"]}
        result, calls = self.run_wrapper(args=["OPERATORS=jobset", "UPDATE_FLAGS=--include-prereleases"],
                                        tags=tags, make=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertIn("v1.1.0-rc.1", result.stdout)
        self.assertIn("New tags in operator groups: jobset", result.stdout)


if __name__ == "__main__":
    unittest.main()
