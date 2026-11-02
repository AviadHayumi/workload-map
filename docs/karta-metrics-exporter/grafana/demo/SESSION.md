<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Setup and restoration record

Recorded on 2026-11-02. This is a historical execution record. Current state must
be checked with the verification commands in [SETUP.md](SETUP.md).

## Exporter release and deployment

1. Inspected the new cluster. Reused its Karta operator, CRDs, Prometheus and fake
   GPU operator. Grafana and the earlier workload generator were absent.
2. Selected exporter commit `9c14b074d03dac9fde4f0b64616d7216a3ad9374` from the fork's
   `exporter-controller-runtime` branch. No exporter source changes were made.
3. Local builds failed when the workstation ran out of disk space. A fork-only
   release build used the exact source commit. `make check`, `helm-lint` and
   `helm-validate` passed. The published binary reported the expected version.
4. Published a prerelease containing a Linux amd64 binary, Helm chart, checksums
   and image digest. The [build run](https://github.com/AviadHayumi/workload-map/actions/runs/41488151152)
   and [release](https://github.com/AviadHayumi/workload-map/releases/tag/exporter-runtime-9c14b074d03d)
   preserve the release evidence. The saved workflow is reference material only;
   it is not enabled on the dashboard branch.
5. Installed the exporter with the operator, webhook and CRD upgrader disabled,
   using `--skip-crds`. The image was pinned by digest.
6. Initial definition selection produced 336 discovery errors because some API
   types/versions were absent. Selected advertised preferred versions and excluded
   missing definitions. Selected cluster Karta definitions remain dynamically watched.
   Revisit the saved exclusions when cluster APIs change.

The exporter source still matched that branch's head when checked after the GPU
restoration. No changes were pushed to the exporter implementation branch during
this setup.

## Grafana and workloads

1. Restored Grafana with anonymous Viewer access, a private generated administrator
   credential held in a Kubernetes Secret, a PVC and HTTPS ingress. No credential
   value is saved in this bundle. Provisioning remains independent of the exporter.
2. Added a ServiceMonitor and recording rules adapting current exporter labels for
   the saved dashboards. Ambiguous or invalid Pod attribution is excluded.
3. Restored bounded workload fixtures and a generator. Jobs are created every two
   minutes; a PyTorchJob is also submitted every six minutes. Resource kinds are
   not randomly selected. Fixtures use real CPU loops and simulated GPU requests.
4. Initially recovered an incomplete dashboard layout. Searched local snapshots
   and 131 fork branch tips. The complete saved layout came from commit
   `5d0661ab19a6830625f26f77faf51cbe3ea5964f`, recorded on 2026-10-15.
5. Restored 55 overview panels, 19 structure panels and three metric-sample panels,
   including collapsed sections. Kept the workload/component/instance/Pod graph
   and separate replica-group edges. Updated metric names and the query catalog.
6. Published the dashboard JSON on `grafana-dashboard`. Query checks initially
   passed even though optional observer metrics were absent. This did not prove
   the activity panels were functioning. Missing data remained Unavailable/Unknown.

## GPU load and activity restoration

1. Recovered the earlier `gpu-demo-randomizer` implementation and successful
   scheduled-run evidence from 2026-10-14. The old job selected workload groups
   every ten minutes and patched simulated utilization ranges every minute.
2. Confirmed the new cluster had the workload generator and DCGM-format exporters,
   but no randomizer or GPU observers. Its 13 steady GPU Pods lacked utilization
   annotations. This explained flat simulated readings and absent activity data.
3. Recovered the helper code into an isolated build directory. Adapted the old
   `pod`/`workload` labels and observation-health inputs to the current exporter
   contract. Invalid observations and explicit workload failures remain failures.
4. Extended the synthetic source/observer path from a single node to an explicit
   multi-node allowlist. Retained per-node freshness, allocation Pod UIDs, complete
   device counts and stale-identity rejection. The running cluster used eight
   configured fake GPU nodes.
5. Used the existing demo label as randomizer opt-in, avoiding workload restarts.
   Its Pod patch permissions are limited to the demo namespace. Source permissions
   allow only reads of the named topology ConfigMaps. The workload observer can
   list demo Pods and read the named round-state ConfigMap.
6. Ran the recovered tests with the race detector. Added metric compatibility and
   multi-node identity checks. Tested the source against fake-gpu-operator commit
   `0901c8fc9c235e5b2ef8a2876fb89db1f7af23f4`. Built separate Linux amd64 helper binaries
   and a nonroot image. The exporter image was unchanged.
7. Validated the manifests with server-side dry-run, then deployed the randomizer,
   source and observers. Prometheus picked up all ten new scrape endpoints after
   its configuration/discovery refresh. No shared fake GPU components were replaced.
8. The scheduled randomizer completed repeatedly. The first round applied quiet
   settings to two steady workloads and active settings to two others, covering
   all 13 steady GPU allocations. Generated short-lived GPU workloads remain
   visible even when they finish before joining a randomizer round.
9. Added explicit time-series query formats and restored timestamped utilization
   and activity data. Moved individual-workload history directly below its graph.
   Updated the dashboard descriptions to reflect the installed helpers.
10. Pushed the working dashboard update as commit
    `12d84d47714cf2d116230e87e0dd97353de3bd25` after `make check` passed.

The newer cluster already had annotation-driven fake GPU simulation. The older
single-node native DCGM/mock-NVML bridge was not installed. The restored source
reports independent synthetic samples from the same operator topology, with UID
identity. Its values need not match the shared exporter sample for sample.

## Verified results and limits

- All 65 dashboard queries executed successfully. At the recorded check, 62 returned
  data. Two optional diagnostics and the discovery-failure query were empty.
- Grafana returned five overview workload series with time fields for both the
  utilization graph and the activity timeline.
- All four steady GPU workloads returned time fields and samples on their detail
  pages. Recorded utilization varied over time, including 62-94% for JAXJob and
  1-2% for quiet workloads.
- The workload observer reported two Active and two Low activity steady workloads
  after the required window. CPU-only/finished workloads were reported separately.
- Eight device-observer targets, one workload-observer target and one source target
  were up. Anonymous Grafana access remained read-only.
- Helper/source race tests and the dashboard branch's `make check` passed.
- Grafana API data frames were inspected. Browser rendering was not inspected.
- No history before the observer deployment was reconstructed.
- Some legacy raw DCGM charts still join by Pod name. The UID-aware workload
  observer is the source for the restored workload activity/history panels.
- The topology allowlist is fixed. Update its configuration and named read
  permissions when adding fake GPU nodes.
- Low utilization does not establish application idleness or safe reclamation.

The 2026-10-12 reproduction guide describes an earlier two-Job experiment with
fixed 90%/1% values, a manual change and a two-minute window. Its 21-workload matrix
and permission-failure experiments were not rerun as part of this restoration.

## Publication checks

The packaged source rebuilt all four Linux amd64 helpers and passed their race
tests. Go source snapshots match the files used for the deployed build. The module
snapshot only removes a trailing blank line; dependencies are unchanged. The original
binaries embedded enclosing checkout VCS metadata; the snapshot rebuild does not.
Binary hashes therefore differ. The pinned deployed image remains the installation
default. [Reproduction checks](evidence/reproduction-checks.json) record both hashes
and the checks performed.

This publication adds the setup, source snapshots and evidence to the existing
`grafana-dashboard` branch. It does not change the running cluster.
