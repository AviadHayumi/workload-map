#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Exercise the CI commit gate against real, isolated Git repositories."""

import hashlib
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("verify_commit.py")
SPEC = importlib.util.spec_from_file_location("verify_commit", SCRIPT)
verifier = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(verifier)


class VerifyCommitTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.manifest = self.root / "recordings.sha256"
        self.env = {
            key: value for key, value in os.environ.items()
            if not key.startswith("GIT_")
        }
        self.env.update({
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_GLOBAL": os.devnull,
        })
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", verifier.BOT_NAME)
        self.git("config", "user.email", verifier.BOT_EMAIL)
        self.git("config", "commit.gpgsign", "false")
        self.write(verifier.ENV_PATH, b'# Pins\nJOBSET_VERSION="${JOBSET_VERSION:-v0.10.1}"\nOTHER=1\n')
        self.write("README.md", b"Unchanged baseline\n")
        self.git("add", ".")
        self.git("commit", "-q", "-s", "-m", "test(e2e): create pilot base")
        self.base = self.git("rev-parse", "HEAD").decode().strip()
        self.write(verifier.ENV_PATH, b'# Pins\nJOBSET_VERSION="${JOBSET_VERSION:-v0.12.0}"\nOTHER=1\n')
        for path in verifier.EXPECTED_CHANGES:
            if path != verifier.ENV_PATH:
                self.write(path, f"flow: {Path(path).stem}\nversion: v0.12.0\n".encode())
        self.git("add", ".")
        self.git("commit", "-q", "-s", "-m", "build(e2e): update jobset")
        self.write_manifest()

    def git(self, *args, input=None):
        return subprocess.run(
            ["git", *args], cwd=self.repo, env=self.env, input=input,
            check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        ).stdout

    def write(self, relative, content):
        path = self.repo / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)

    def write_manifest(self):
        self.manifest.write_text("".join(
            f"{hashlib.sha256(self.git('show', 'HEAD:' + path)).hexdigest()}  {path}\n"
            for path in verifier.EXPECTED_CHANGES
        ))

    def amend(self):
        self.git("add", "-A")
        self.git("commit", "-q", "--amend", "--no-edit")

    def run_gate(self, *args):
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--manifest", str(self.manifest),
             "--base-sha", self.base, *args], cwd=self.repo, env=self.env,
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )

    def assert_rejected(self, message):
        result = self.run_gate()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn(message, result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def test_accepts_exact_bot_commit_and_manifest(self):
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("five recordings", result.stdout)

    def test_verifies_git_blobs_instead_of_mutable_worktree(self):
        self.write(verifier.ENV_PATH, b"uncommitted checkout change\n")
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejects_committed_recording_tampered_after_test(self):
        path = next(path for path in verifier.EXPECTED_CHANGES if path != verifier.ENV_PATH)
        self.write(path, b"tampered after recording validation\n")
        self.amend()
        self.assert_rejected("differs from the tested content")

    def test_rejects_extra_or_missing_manifest_paths(self):
        original = self.manifest.read_text()
        variants = [
            "\n".join(original.splitlines()[1:]) + "\n",
            original + "0" * 64 + "  README.md\n",
        ]
        for value in variants:
            with self.subTest(value=value):
                self.manifest.write_text(value)
                self.assert_rejected("manifest must contain exactly")

    def test_rejects_duplicate_manifest_path(self):
        original = self.manifest.read_text()
        self.manifest.write_text(original + original.splitlines()[0] + "\n")
        self.assert_rejected("duplicate path")

    def test_rejects_unsafe_and_malformed_manifest_entries(self):
        original = self.manifest.read_text()
        for path in ("../escape", "/absolute", "./hack/e2e/global.env", "hack//e2e/global.env"):
            with self.subTest(path=path):
                self.manifest.write_text(original + "0" * 64 + "  " + path + "\n")
                self.assert_rejected("unsafe path")
        for line in ("0" * 64 + " *hack/e2e/global.env", "bad  hack/e2e/global.env", ""):
            with self.subTest(line=line):
                self.manifest.write_text(original + line + "\n")
                self.assert_rejected("standard SHA-256")

    def test_rejects_relative_missing_and_symlink_manifest(self):
        for path in (Path("relative.sha256"), self.root / "missing"):
            with self.subTest(path=path):
                result = self.run_gate("--manifest", str(path))
                self.assertEqual(result.returncode, 1)
                self.assertIn("absolute path to a regular file", result.stderr)
        link = self.root / "linked.sha256"
        link.symlink_to(self.manifest)
        result = self.run_gate("--manifest", str(link))
        self.assertEqual(result.returncode, 1)
        self.assertIn("absolute path to a regular file", result.stderr)

    def test_rejects_extra_committed_file(self):
        self.write("unexpected.txt", b"extra\n")
        self.amend()
        self.assert_rejected("exactly the pin and five new recordings")

    def test_rejects_missing_committed_recording(self):
        (self.repo / next(path for path in verifier.EXPECTED_CHANGES if path != verifier.ENV_PATH)).unlink()
        self.amend()
        self.assert_rejected("exactly the pin and five new recordings")

    def test_rejects_modified_preexisting_recording(self):
        tree = self.git("rev-parse", "HEAD^{tree}").decode().strip()
        self.git("checkout", "--detach", self.base)
        path = next(path for path in verifier.EXPECTED_CHANGES if path != verifier.ENV_PATH)
        self.write(path, b"preexisting recording\n")
        self.git("add", path)
        self.git("commit", "-q", "-s", "-m", "test(e2e): existing recording")
        self.base = self.git("rev-parse", "HEAD").decode().strip()
        message = f"build(e2e): update jobset\n\nSigned-off-by: {verifier.BOT_IDENTITY}\n"
        candidate = self.git("commit-tree", tree, "-p", self.base, input=message.encode()).decode().strip()
        self.git("reset", "--hard", candidate)
        self.assert_rejected("add the five new recordings")

    def test_rejects_other_env_change_even_with_matching_manifest(self):
        self.write(verifier.ENV_PATH, b'# Pins\nJOBSET_VERSION="${JOBSET_VERSION:-v0.12.0}"\nOTHER=2\n')
        self.amend()
        self.write_manifest()
        self.assert_rejected("change only JobSet")

    def test_rejects_non_bot_author(self):
        self.git("commit", "-q", "--amend", "--no-edit", "--author", "Example <example@example.com>")
        self.assert_rejected("authored by github-actions[bot]")

    def test_rejects_missing_or_spoofed_dco_trailer(self):
        messages = [
            "build(e2e): update jobset",
            "build(e2e): update jobset\n\nSigned-off-by: Example <example@example.com>",
            f"build(e2e): update jobset\n\nSigned-off-by: {verifier.BOT_IDENTITY}\n\nThis is body text after the supposed trailer.",
        ]
        for message in messages:
            with self.subTest(message=message):
                self.git("commit", "-q", "--amend", "-m", message)
                self.assert_rejected("DCO trailer")

    def test_rejects_multiple_candidate_commits(self):
        self.git("commit", "-q", "--allow-empty", "-s", "-m", "test(e2e): extra commit")
        self.assert_rejected("exactly one parent equal")

    def test_rejects_merge_commit(self):
        tree = self.git("rev-parse", "HEAD^{tree}").decode().strip()
        candidate = self.git("rev-parse", "HEAD").decode().strip()
        message = f"build(e2e): merge candidate\n\nSigned-off-by: {verifier.BOT_IDENTITY}\n"
        merged = self.git("commit-tree", tree, "-p", self.base, "-p", candidate, input=message.encode()).decode().strip()
        self.git("reset", "--hard", merged)
        self.assert_rejected("exactly one parent equal")

    def test_rejects_non_regular_recording_mode(self):
        path = next(path for path in verifier.EXPECTED_CHANGES if path != verifier.ENV_PATH)
        self.git("update-index", "--chmod=+x", path)
        self.git("commit", "-q", "--amend", "--no-edit")
        self.assert_rejected("regular, non-executable")

    def test_rejects_bad_base_sha_and_handles_git_error_cleanly(self):
        result = self.run_gate("--base-sha", "HEAD~1")
        self.assertEqual(result.returncode, 1)
        self.assertIn("40 lowercase hexadecimal", result.stderr)
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--manifest", str(self.manifest), "--base-sha", self.base],
            cwd=self.root, env=self.env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("Git could not inspect", result.stderr)
        self.assertNotIn("Traceback", result.stderr)


if __name__ == "__main__":
    unittest.main()
