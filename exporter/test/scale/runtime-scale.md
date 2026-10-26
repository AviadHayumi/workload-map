<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Scale checks through the exporter executable

`runtime_scale.py` launches a supplied exporter binary. A loopback HTTP fixture
serves Kubernetes discovery, paginated LIST/GET responses and streaming WATCH
events. The executable runs its real informers, workqueue, Discovery, inventory,
SDK evaluation, publication and OTel/Prometheus handler. The harness never inserts
records into exporter caches or calls replacement discovery logic.

This is a modeled Kubernetes API. It does not run etcd, authentication, admission,
a scheduler, or an actual cluster. It does not establish production capacity.
The fixture generates full Jobs and Pods lazily, so fixture memory stays separate
from exporter process RSS. Catalog `batch-job-v1` is enabled; cluster definitions
are disabled. All nine phases are requested. Roots and Pods carry the synthetic
submitter `scale-user@example.test`. An explicit CEL expression reads the
`karta.run.ai/submitted-by` annotation. The oracle requires that identity on
every status and Pod row, so a valid root cannot pass while complete-membership
verification is missing.

Build the assembled exporter and start with 10k roots. Run from the repository
root. Use an output directory outside tracked source files:

```sh
(cd exporter && go build -o /tmp/karta-exporter ./cmd)
python3 exporter/test/scale/runtime_scale.py \
  --binary /tmp/karta-exporter --roots 10000 --pods 1 \
  --output /tmp/karta-scale-10000-1
```

The target matrix is 100k roots with 0, 1 and 10 Pods each. Increase only after a
smaller case fits the available resources. A separate huge-root case uses
`--roots 1 --pods 100000`. Each run has a sampled RSS cap, wall-time cap and
per-stage deadline. Defaults are 8 GiB, 480 seconds and 180 seconds. The child
uses four Go processors and a Go memory target at 75% of the RSS cap. Limits
kill the child and retain a failed result with partial observations. RSS is
sampled once per second, so this is not a hard OS memory limit.

Stages cover cold start, two steady scrapes, an update to the last Pod of the
first root, status and Pod
updates for 100 roots, deletion of 10% of roots and their Pods, and a final
measurement after 30 seconds. The huge-root case deletes its sole root. The
streaming oracle checks every UID, all nine status values, creation timestamps,
exact metric label sets, component coordinates, observation validity and
resource-type health. `TestRuntimeFixtureCatalogExpectations` checks the catalog
SDK expectations: Running or Failed, component `job`, and empty instance and
replica group. A run
passes only when each stage reaches complete, current output and every steady
scrape is valid. `completed_all_stages` can be true while `complete` is false if
steady output temporarily loses validity. Recovery does not erase those failures. The isolated Pod
update changes only its resourceVersion. Its public values stay unchanged. This
stage waits for WATCH delivery and a metadata LIST at the new version, then
checks output invariance. It does not measure exact event-to-publication latency.

`result.json` records the binary and harness SHA-256, identity configuration, complete or partial stage counts,
response sizes, HTTP timings, request counts by resource/operation/representation,
watch events, sampled RSS and available Go process metrics. Scrape timing includes
network transfer and the Python streaming oracle; it is not callback-only time.
Convergence latency includes the polling interval and scrape time. Workqueue age
and callback backlog are not exposed by the executable and are not measured.
RSS is an observed sample maximum, not the operating system's exact high-water
mark. Post-deletion RSS can retain Go heap reservations.

The API fixture honors bounded pagination and keeps 16 compact snapshots of
deleted flags and resource versions. Continuations retain their original state
across concurrent changes. Evicted snapshots return HTTP 410. Full object bodies
are generated per page. Watches close normally after 240 seconds. The bounded
watch history returns HTTP-watch `Expired`
events when a slow reader falls behind. This can force real informer relists
during deletion. Counters record these expirations. No external kubeconfig or
cluster is used; the generated kubeconfig points only to the loopback fixture
and contains no credentials.

Verify the fixture and independent oracle without building the exporter:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s exporter/test/scale -p runtime_scale_test.py -v
```

The older Go store/collector scale harness remains separate. Its results do not
include this executable's cold start, watches or workqueue.
