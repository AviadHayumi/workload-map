#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Verify that Renovate committed exactly the pin and recordings tested in CI."""

import argparse
import hashlib
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys


BOT_NAME = "github-actions[bot]"
BOT_EMAIL = "41898282+github-actions[bot]@users.noreply.github.com"
BOT_IDENTITY = f"{BOT_NAME} <{BOT_EMAIL}>"
ENV_PATH = "hack/e2e/global.env"
RECORDING_DIR = (
    "test/e2e/recorded_data/jobset/v0.12.0/jobset-x-k8s-io-jobset-v1alpha2"
)
EXPECTED_CHANGES = {
    ENV_PATH: "M",
    **{f"{RECORDING_DIR}/{flow}.yaml": "A" for flow in (
        "running", "completed", "failed", "resumed", "suspended"
    )},
}


def git(repo, *args):
    return subprocess.run(
        ["git", *args], cwd=repo, check=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    ).stdout


def verify(manifest, base_sha, repo):
    if not re.fullmatch(r"[0-9a-f]{40}", base_sha):
        raise ValueError("base SHA must contain exactly 40 lowercase hexadecimal digits")
    if not manifest.is_absolute() or not manifest.is_file() or manifest.is_symlink():
        raise ValueError("manifest must be an absolute path to a regular file")

    parents = git(repo, "show", "-s", "--format=%P", "HEAD").decode("ascii").split()
    if parents != [base_sha]:
        raise ValueError("HEAD must have exactly one parent equal to the verified base SHA")
    author = git(repo, "show", "-s", "--format=%an%x00%ae", "HEAD").decode("utf-8").rstrip("\n")
    if author != f"{BOT_NAME}\0{BOT_EMAIL}":
        raise ValueError("HEAD must be authored by github-actions[bot]")
    trailers = git(
        repo, "show", "-s", "--format=%(trailers:key=Signed-off-by,valueonly)", "HEAD"
    ).decode("utf-8").splitlines()
    if BOT_IDENTITY not in trailers:
        raise ValueError("HEAD must contain the github-actions[bot] DCO trailer")

    raw_changes = git(
        repo, "diff-tree", "--no-commit-id", "--name-status", "-r", "-z",
        "--no-renames", base_sha, "HEAD", "--",
    ).decode("utf-8").split("\0")
    if raw_changes[-1] != "" or len(raw_changes) != 2 * len(EXPECTED_CHANGES) + 1:
        raise ValueError("HEAD must change exactly the pin and five new recordings")
    changes = dict(zip(raw_changes[1:-1:2], raw_changes[:-1:2]))
    if changes != EXPECTED_CHANGES:
        raise ValueError("HEAD must modify only global.env and add the five new recordings")
    entries = git(repo, "ls-tree", "-r", "-z", "HEAD", "--", *EXPECTED_CHANGES).split(b"\0")
    if len(entries) != len(EXPECTED_CHANGES) + 1 or entries[-1] != b"":
        raise ValueError("HEAD does not contain all six expected files")
    for entry in entries[:-1]:
        metadata, path = entry.split(b"\t", 1)
        mode, kind, _ = metadata.split()
        if mode != b"100644" or kind != b"blob" or path.decode("ascii") not in EXPECTED_CHANGES:
            raise ValueError("candidate paths must be regular, non-executable Git files")

    hashes = {}
    for line in manifest.read_bytes().decode("ascii").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_./-]+)", line)
        if match is None:
            raise ValueError("manifest must contain standard SHA-256 lines with two spaces")
        digest, path = match.groups()
        parsed = PurePosixPath(path)
        if parsed.is_absolute() or ".." in parsed.parts or parsed.as_posix() != path:
            raise ValueError("manifest contains an unsafe path")
        if path in hashes:
            raise ValueError("manifest contains a duplicate path")
        hashes[path] = digest
    if hashes.keys() != EXPECTED_CHANGES.keys():
        raise ValueError("manifest must contain exactly the pin and five new recordings")
    for path, expected_hash in hashes.items():
        # The manifest describes published content, independent of checkout changes.
        actual_hash = hashlib.sha256(git(repo, "show", f"HEAD:{path}")).hexdigest()
        if actual_hash != expected_hash:
            raise ValueError(f"committed content differs from the tested content: {path}")

    before = git(repo, "show", f"{base_sha}:{ENV_PATH}")
    old_pin = rb'^JOBSET_VERSION="\$\{JOBSET_VERSION:-v0\.10\.1\}"$'
    new_pin = b'JOBSET_VERSION="${JOBSET_VERSION:-v0.12.0}"'
    expected_env, replacements = re.subn(old_pin, new_pin, before, flags=re.MULTILINE)
    if replacements != 1 or git(repo, "show", f"HEAD:{ENV_PATH}") != expected_env:
        raise ValueError("global.env must change only JobSet v0.10.1 to v0.12.0")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--base-sha", required=True)
    args = parser.parse_args(argv)
    try:
        verify(args.manifest, args.base_sha, Path.cwd())
    except (OSError, UnicodeError, ValueError, subprocess.CalledProcessError) as error:
        message = "Git could not inspect the candidate commit" if isinstance(
            error, subprocess.CalledProcessError
        ) else str(error)
        print(f"verify-commit: {message}", file=sys.stderr)
        return 1
    print("Verified the CI bot commit contains exactly the tested pin and five recordings")
    return 0


if __name__ == "__main__":
    sys.exit(main())
