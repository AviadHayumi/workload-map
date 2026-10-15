<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics lab

The lab connects Karta workload identity to GPU measurements in Prometheus and
Grafana. The workload overview comes first, with links to Components, Pods, raw
metric samples, and the exact PromQL. Separate activity observers leave Karta's
public metrics and Helm values unchanged.

- [Real A100 findings and screenshots](real-gpu/A100-REPORT.md): tested workloads,
  failures, repairs, full GPUs, static MIG, DRA, and restoration evidence.
- [Physical GPU setup](real-gpu/README.md): native DCGM, dashboards, and finite
  CUDA workloads, followed by [MIG](real-gpu/mig/README.md) and
  [DRA](real-gpu/dra/README.md).
- [Complete playground setup](playground-dev/recreate/README.md): fake GPU
  allocation with native NVIDIA DCGM reading mock NVML, pinned operator inputs,
  controllable activity, and finite workload submission.
- [Observer source and tests](../../exporter/examples/workload-gpu-observer):
  Pod UID attribution, complete allocation checks, and low-activity windows.

The physical GPU tests passed on two A100s on one worker. The observer reports
low activity only after all assigned devices stay at or below 5% throughout
180 seconds of valid observations. Missing evidence is Unknown. This does not
establish application idleness, safe reclamation, or production scale.

## Publication and evidence

This directory contains the portable inputs and selected evidence from the
2026-10-14/15 runs. Eleven original Grafana screenshots are included without
image edits. Metric values, timestamps, test outcomes, and workload identities
are retained. Text evidence replaces private IP addresses with documentation
addresses and node names with aliases. These aliases are not connection details.

Kubeconfigs, credentials, unrelated cluster inventories, private before-state
snapshots, and local build directories are excluded. Capture fresh rollback
state on the target cluster. Historical evidence files describe the recorded
run; running a verification script writes new evidence for the selected cluster.
Keep the recorded directory unchanged and use a rendered working copy.

The playground bundle includes pinned upstream charts and manifests. Their
original licenses and notices remain with the files. `verify_bundle.py` checks
their captured hashes. It has rendering and API dry-run evidence, not a claim
that a second empty cluster was deployed from this published copy.
