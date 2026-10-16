# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Rebuild matched exporter binaries without changing any Git state."""
from pathlib import Path
import hashlib
import json
import os
import subprocess
import argparse

ROOT = Path(__file__).resolve().parents[4]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("output", type=Path, help="Directory for binaries, historical sources and provenance")
OUT = parser.parse_args().output.resolve()
OUT.mkdir(parents=True, exist_ok=True)
BASE = "5d0661ab19a6830625f26f77faf51cbe3ea5964f"
EXTRACTION = "91d6d8ae51c741e7dc65ef1776d8e44a481b4d93"
SOURCES = ["exporter/pkg/owner/index.go", "exporter/pkg/attribute/attributor.go"]
ENV = dict(os.environ, GOMAXPROCS="2", GOGC="100")


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def main():
    parent = git("rev-parse", EXTRACTION + "^").decode().strip()
    assert parent == BASE, (parent, BASE)
    records, replacements, controls = [], {}, []
    head = git("rev-parse", "HEAD").decode().strip()
    for path in SOURCES:
        historical = git("show", BASE + ":" + path)
        dest = OUT / "baseline" / path
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(historical)
        replacements[str(ROOT / path)] = str(dest)
        records.append({"source_path": path, "baseline_revision": BASE,
            "baseline_sha256": digest(historical),
            "baseline_git_blob": git("rev-parse", BASE + ":" + path).decode().strip(),
            "current_sha256": digest((ROOT / path).read_bytes()),
            "current_git_blob": git("rev-parse", head + ":" + path).decode().strip()})
    for path in git("ls-files", "exporter", "charts/karta").decode().splitlines():
        if path in SOURCES or not ((path.endswith(".go") and not path.endswith("_test.go"))
                or path in ["exporter/go.mod", "exporter/go.sum"] or path.startswith("charts/karta/")):
            continue
        current = (ROOT / path).read_bytes()
        assert git("show", BASE + ":" + path) == current, "Uncontrolled production difference: " + path
        controls.append({"path": path, "sha256": digest(current)})
    overlay = OUT / "legacy-overlay.json"
    overlay.write_text(json.dumps({"Replace": replacements}, indent=2) + "\n")
    harness = "exporter/pkg/controller/sdk_performance_test.go"
    report = {"baseline_revision": BASE, "extraction_revision": EXTRACTION,
        "extraction_parent": parent, "current_revision": head,
        "shared_code_revision": head,
        "shared_code_dirty": bool(git("status", "--porcelain", "pkg")),
        "overlay_sources": records, "byte_identical_controls": controls,
        "harness_path": harness, "harness_sha256": digest((ROOT / harness).read_bytes()),
        "go_version": subprocess.check_output(["go", "version"], text=True).strip(),
        "scope": "Matched historical owner/attribution algorithms on current dependencies; not a complete historical application rebuild.",
        "module_files": {path: digest((ROOT/path).read_bytes()) for path in ["go.mod", "go.sum", "exporter/go.mod", "exporter/go.sum"]},
        "shared_code_files": {str(path.relative_to(ROOT)): digest(path.read_bytes())
            for directory in ["pkg/ownership", "pkg/attribution", "pkg/resource", "pkg/instructions", "pkg/execution", "pkg/catalog"]
            for path in (ROOT/directory).rglob("*.go") if not path.name.endswith("_test.go")},
        "dependency_note": "Root go.mod/go.sum gained discovery dependencies after baseline. Exporter module files are unchanged. Both binaries use the same current module resolution. Existing resource factory code is unchanged; an additional constructor was added."}
    (OUT / "source-provenance.json").write_text(json.dumps(report, indent=2) + "\n")
    for mode in ["legacy", "sdk"]:
        command = ["go", "test", "-p", "2"]
        if mode == "legacy":
            command += ["-overlay", str(overlay)]
        command += ["-c", "-o", str(OUT / (mode + ".test")), "./pkg/controller"]
        subprocess.run(command, cwd=ROOT / "exporter", env=ENV, check=True)
    (OUT / "binary-hashes.json").write_text(json.dumps({mode: digest((OUT / (mode + ".test")).read_bytes()) for mode in ["legacy", "sdk"]}, indent=2) + "\n")


if __name__ == "__main__":
    main()
