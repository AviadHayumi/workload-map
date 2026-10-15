<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Real GPU validation result

Both two-GPU PyTorchJobs completed successfully. All live acceptance checks passed.

| Check | Result |
| --- | --- |
| Physical devices | Two NVIDIA A100 PCIe 40 GB GPUs |
| Maximum measured GPU utilization | 98% |
| Quiet worker GPU during compute | 0% |
| Whole workload with one busy component | Active |
| Sustained low activity | All assigned GPUs <=5% for at least 180 seconds |
| Earliest low classification in repeat run | 180.002 seconds of evidence |
| Resumed GPU computation | Changed back to Active |
| Workload completion | Both PyTorchJobs Succeeded; GPU allocations released |
| Karta Pod attribution | Resolved; no ambiguous Pod UID mapping |
| Scrapes and Karta observation health | Healthy |
| Dashboard queries | 57 valid; component GPU queries return data |
| Grafana UI | Overview, UID structure drill-down, and raw DCGM samples passed |
| GPU observer tests | Existing and native tests passed with race detector |

Native profiling also measured GR engine activity of 0.979056 and tensor-pipeline
activity of 0.860545 during matrix multiplication. These are actual DCGM ratios.

The workload GPU observer is separate from Karta. Native DCGM has no per-sample
collection timestamp in this setup, so the window measures successful HTTP reads.
This run validates CUDA compute and attribution, not full model training or NIM
inference. Karta's public metrics and Helm API were unchanged.

See `acceptance.json`, `query-validation.json`, and `browser-acceptance.json` for
the machine-readable evidence. `README.md` explains recreation and the finite
one-GPU CronJob, which is scheduled every 15 minutes.

## Knative routing repaired

The original HTTPRoutes pointed to nonexistent Istio gateways. Dedicated
kgateway Gateways and ClusterIP Services now serve Knative external/local routes.
The KnativeServing operator configuration references those actual gateways.
Service Ready and RoutesReady became True, and a real routed request returned
HTTP 200 with `Hello smoke!`. The shared TLS gateway and MetalLB pool were
preserved. The tested external route is accessed through a local port forward.

Recreate with `knative-fix/gateways.json` and the operator patch in
`knative-fix/serving-patch.json`, after verifying the installed kgateway class
controller and KnativeServing resource names. See `knative-fix/http-acceptance.json`.
NIM model serving was not exercised.

[Open the recorded activity window in Grafana](http://127.0.0.1:13000/karta/grafana/d/karta-workload-overview/karta-real-gpu?from=1792007560217&to=1792008460217).

## Final mixed A100 and MIG result

One A100 remains whole; the other provides two 3g.20gb MIG instances. A drained,
approved worker reboot resolved MIG activation. All six nodes recovered Ready.
The original finite GPU CronJob was resumed after validation.

`real-pytorch-mig-probe`, `real-pytorch-mig-verified`, `real-full-with-mig`,
`real-pytorch-mig-timed`, and `real-full-mig-timed` all completed successfully.
The final two runs passed all 12 acceptance checks, 57 dashboard query checks,
and browser validation. Both showed Active, Warming, Low activity, resumed
Active, then No GPUs. Low activity required at least 180 seconds of evidence.

MIG uses native `DCGM_FI_PROF_GR_ENGINE_ACTIVE`, with GPU instance and compute
instance IDs preserved. Physical power is deduplicated by GPU UUID. Karta
public metrics and Helm values remain unchanged.

The initial DCGM collection variable was invalid and silently ignored. The
baseline actually used the 30-second default. It was corrected to
`DCGM_EXPORTER_INTERVAL=5000`, the exporter was rolled, and final activity/timing
tests were repeated. See `mig/interval-fixed/timing.json` for measured delays.

`A100-REPORT.md` contains screenshots, every workload, failures, fixes, hardware
observations, and scope limits. `mig/README.md` explains reproduction.
