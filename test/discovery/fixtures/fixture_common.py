# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Explicit cluster selection and local output boundaries for discovery fixtures."""

import os
import pathlib
import subprocess

BASE = pathlib.Path(__file__).resolve().parent
ROOT = BASE.parents[2]
NS = "sdk-discovery-test"
KUBE = None


def validate_environment():
    """Require an explicit kubeconfig and a named kind context before any API call."""
    global KUBE
    raw = os.environ.get("KARTA_DISCOVERY_KUBECONFIG")
    if not raw:
        raise SystemExit("Set KARTA_DISCOVERY_KUBECONFIG to the dedicated kind kubeconfig")
    kubeconfig = pathlib.Path(raw).expanduser().resolve()
    if not kubeconfig.is_file():
        raise SystemExit("KARTA_DISCOVERY_KUBECONFIG must name an existing file")
    context = os.environ.get("KARTA_DISCOVERY_CONTEXT", "kind-karta-sdk-discovery")
    if not context.startswith("kind-") or context == "kind-":
        raise SystemExit("KARTA_DISCOVERY_CONTEXT must select a kind-* context")
    KUBE = ["kubectl", "--kubeconfig", str(kubeconfig), "--context", context]
    names = run("config", "get-contexts", context, "-o", "name").splitlines()
    if names != [context]:
        raise SystemExit("The selected kind context is absent from the dedicated kubeconfig")


def run(*args, body=None):
    if KUBE is None:
        raise RuntimeError("validate_environment must precede kubectl calls")
    return subprocess.check_output(KUBE + list(args), input=body, text=True)


def oracle_path():
    """Keep incarnation-specific UIDs in ignored local output, outside fixtures."""
    raw = os.environ.get("KARTA_DISCOVERY_ORACLE")
    if not raw:
        raise SystemExit("Set KARTA_DISCOVERY_ORACLE to an ignored file under .context/")
    output = pathlib.Path(raw).expanduser().resolve()
    local = ROOT / ".context"
    if not output.is_relative_to(local) or output == local:
        raise SystemExit("KARTA_DISCOVERY_ORACLE must be a file under this repository's .context/")
    ignored = subprocess.run(
        ["git", "-C", str(ROOT), "check-ignore", "--quiet", "--", str(output.relative_to(ROOT))],
        check=False,
    )
    if ignored.returncode != 0:
        raise SystemExit("Ignore .context/ in the repository's local Git exclude before writing the oracle")
    return output
