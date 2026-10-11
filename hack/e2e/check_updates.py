#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Report upstream operator tags without changing pins or contacting Kubernetes."""

import argparse
import os
import re
import subprocess
import sys
from urllib.parse import quote


SEMVER = re.compile(
    r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
    r"(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
)
HEADER = "STATUS   OPERATOR             PINNED                       LATEST TAG"


def version_key(version):
    """SemVer precedence, including numeric prereleases and ignored build metadata."""
    match = SEMVER.fullmatch(version)
    if not match:
        raise ValueError(f"invalid semantic version: {version}")
    major, minor, patch, prerelease, _ = match.groups()
    identifiers = []
    if prerelease:
        for part in prerelease.split("."):
            if part.isdigit():
                if len(part) > 1 and part.startswith("0"):
                    raise ValueError(f"invalid numeric prerelease: {version}")
                identifiers.append((0, int(part)))
            else:
                identifiers.append((1, part))
    return (int(major), int(minor), int(patch), prerelease is None, tuple(identifiers))


def release_key(version, channel):
    # Grove's alpha.N-rcM precedes alpha.N, although plain SemVer would put the
    # mixed numeric/text identifier after N. Preserve the upstream promotion order.
    if channel == "alpha":
        match = re.fullmatch(r"(\d+\.\d+\.\d+-alpha\.\d+)(?:-rc(\d+))?", version)
        if match:
            base, rc = match.groups()
            version = f"{base}.0.rc.{rc}" if rc else f"{base}.1"
    # Several upstreams increment undotted rcN (and alphaN/betaN) counters.
    # Compare the counter numerically, as their release scripts do.
    version = re.sub(r"-(alpha|beta|rc)([0-9]+)$", r"-\1.\2", version)
    return version_key(version)


def remote_tags(repository):
    result = subprocess.run(
        ["git", "-c", "credential.helper=", "ls-remote", "--tags", "--refs",
         f"https://github.com/{repository}.git"],
        capture_output=True, text=True, timeout=60,
        env={**os.environ, "GIT_TERMINAL_PROMPT": "0"},
    )
    if result.returncode:
        raise ValueError(f"cannot read tags from {repository}; check network/repository access")
    return [line.split("refs/tags/", 1)[1] for line in result.stdout.splitlines()
            if "refs/tags/" in line]


def check_operator(name, pinned, repository, prefix, channel, note, include_prereleases, excluded_tags=()):
    if channel not in ("stable", "alpha"):
        raise ValueError(f"invalid release channel: {channel}")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError(f"invalid GitHub repository: {repository}")
    # Pins are installer versions (often bare or v-prefixed), while a chart or
    # manifest may use a different tag namespace in the same repository.
    version = pinned.removeprefix(prefix) if prefix and pinned.startswith(prefix) else pinned.removeprefix("v")
    current = release_key(version, channel)
    candidates = []
    tags = remote_tags(repository)
    for tag in tags:
        if tag in excluded_tags or not tag.startswith(prefix):
            continue
        try:
            key = release_key(tag[len(prefix):], channel)
        except ValueError:
            continue
        is_alpha = channel == "alpha" and re.fullmatch(r"\d+\.\d+\.\d+-alpha\.\d+", tag[len(prefix):])
        if key[3] or is_alpha or include_prereleases:
            candidates.append((key, tag))
    if not candidates:
        raise ValueError(f"no matching {channel} tags in {repository} (prefix {prefix!r})")
    latest_key, latest_tag = max(candidates)
    newer = sum(key > current for key, _ in candidates)
    status = "NEW" if newer else "AHEAD" if current > latest_key else "CURRENT"
    print(f"{status:<8} {name:<20} {pinned:<28} {latest_tag}")
    base = f"https://github.com/{repository}"
    print(f"  Tags: {base}/tags")
    if newer:
        current_tag = f"{prefix}{version}"
        print(f"  {newer} newer tag(s)")
        if current_tag in tags:
            print(f"  Changes: {base}/compare/{quote(current_tag, safe='')}...{quote(latest_tag, safe='')}")
        else:
            print(f"  Pinned tag {current_tag} was not found; no comparison is available.")
    if note:
        print(f"  Note: {note}")
    return bool(newer)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--operator", action="append", nargs=6, required=True,
                        metavar=("NAME", "PIN", "REPO", "PREFIX", "CHANNEL", "NOTE"))
    parser.add_argument("--include-prereleases", action="store_true",
                        help="also report prerelease tags (Grove includes alpha releases by default)")
    parser.add_argument("--fail-on-update", action="store_true",
                        help="exit 2 when updates are found; lookup errors always exit 1")
    parser.add_argument("--exclude-tag", action="append", default=[],
                        help="exclude a known retracted upstream tag (repeatable)")
    parser.add_argument("--no-header", action="store_true", help=argparse.SUPPRESS)
    try:
        args = parser.parse_args(argv)
    except SystemExit as error:
        # Reserve exit 2 for detected updates, including in standalone wrappers.
        return 1 if error.code else 0
    if not args.no_header:
        print(HEADER, flush=True)
    updated = failed = False
    for operator in args.operator:
        try:
            updated = check_operator(*operator, args.include_prereleases, args.exclude_tag) or updated
        except (ValueError, OSError, subprocess.TimeoutExpired) as error:
            name, pinned = operator[:2]
            print(f"{'ERROR':<8} {name:<20} {pinned:<28} -", flush=True)
            print(f"  {name}: {error}", file=sys.stderr)
            failed = True
    return 1 if failed else 2 if updated and args.fail_on_update else 0


if __name__ == "__main__":
    sys.exit(main())
