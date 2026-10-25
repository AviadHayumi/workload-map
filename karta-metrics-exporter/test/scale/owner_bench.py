#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Run serial warm owner-walk microbenchmarks through temporary Go overlays.

No comparator checkout is edited. Setup and cache warming are excluded from
ns/op and allocation measurements. This is not a full-runtime comparison.
"""

import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import tempfile

BASELINE = "01352db5c4be54b0b3eb6d53a0a04fb5faa34520"
WORKLOAD_DISCOVERY = "1f56e5bf50a33c943ce5c7d45a19a7ff003c5bbf"


def command(*args, cwd):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--current", required=True, help="Karta checkout root")
    parser.add_argument("--baseline", required=True, help="Exact old PoC checkout root")
    parser.add_argument("--workload-discovery", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--roots", type=int, default=1000)
    parser.add_argument("--benchtime", default="300ms")
    parser.add_argument("--count", type=int, default=3)
    args = parser.parse_args()
    if not 1 <= args.roots <= 100000 or not 1 <= args.count <= 20:
        parser.error("roots must be 1..100000 and count 1..20")
    output = pathlib.Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    templates = pathlib.Path(__file__).resolve().parent / "ownership"
    common = (templates / "common.go.txt").read_text()
    scenarios = [
        ("current", pathlib.Path(args.current).resolve(), "karta-metrics-exporter", "pkg/discovery", None),
        ("baseline", pathlib.Path(args.baseline).resolve(), "exporter", "pkg/discovery", BASELINE),
        ("workload_discovery", pathlib.Path(args.workload_discovery).resolve(), ".", "internal/controller", WORKLOAD_DISCOVERY),
    ]
    manifest = {"scope": "healthy acyclic warm owner walk; no API transport/evaluation/publication", "roots": args.roots, "gomaxprocs": 4, "benchtime": args.benchtime, "count": args.count, "runs": []}
    for name, checkout, module, package, expected in scenarios:
        revision = command("git", "rev-parse", "HEAD", cwd=checkout)
        dirty = command("git", "status", "--porcelain", cwd=checkout)
        if expected and (revision != expected or dirty):
            raise SystemExit(f"{name} must be clean at {expected}; got {revision}, dirty={bool(dirty)}")
        module = checkout / module
        source = (templates / (name + ".go.txt")).read_text() + "\n" + common
        run = {"name": name, "commit": revision, "dirty": bool(dirty), "go_version": command("go", "version", cwd=module), "go_mod_sha256": hashlib.sha256((module / "go.mod").read_bytes()).hexdigest(), "template_sha256": hashlib.sha256(source.encode()).hexdigest()}
        package_digest = hashlib.sha256()
        for path in sorted((module / package).glob("*.go")):
            package_digest.update(path.name.encode())
            package_digest.update(path.read_bytes())
        run["package_sha256"] = package_digest.hexdigest()
        manifest["runs"].append(run)
        with tempfile.TemporaryDirectory(prefix="karta-owner-bench-") as temp:
            temp = pathlib.Path(temp)
            replacement = temp / "owner_bench_test.go"
            replacement.write_text(source)
            subprocess.run(["gofmt", "-w", str(replacement)], check=True)
            run["overlay_sha256"] = hashlib.sha256(replacement.read_bytes()).hexdigest()
            overlay = temp / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(module / package / "scale_owner_overlay_test.go"): str(replacement)}}))
            invocation = ["go", "test", "-mod=readonly", "-overlay=" + str(overlay), "./" + package, "-run=^$", "-bench=^BenchmarkScaleOwnerWalk$", "-benchmem", "-benchtime=" + args.benchtime, "-count=1"]
            run["command"] = [part for part in invocation if not part.startswith("-overlay=")]
            env = dict(os.environ, GOMAXPROCS="4", GOCACHE="/tmp/karta-layout-go-cache", OWNER_BENCH_ROOTS=str(args.roots), GOWORK="off")
            with (output / (name + ".txt")).open("w") as log:
                run["repetition_exit_codes"] = []
                for _ in range(args.count):
                    result = subprocess.run(invocation, cwd=module, env=env, stdout=log, stderr=subprocess.STDOUT)
                    run["repetition_exit_codes"].append(result.returncode)
                    if result.returncode:
                        break
            run["exit_code"] = result.returncode
        (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        if result.returncode:
            raise SystemExit(f"{name} failed; inspect {output / (name + '.txt')}")
        print(f"{name}: completed", flush=True)


if __name__ == "__main__":
    main()
