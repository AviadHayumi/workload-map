<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Connected exporter acceptance

This harness runs the actual compiled exporter command against an explicitly
selected disposable Kubernetes cluster. It uses Python's standard library,
`kubectl`, and HTTP exposition. It does not substitute a controller or populate
the publication store directly.

The cluster must already have established Karta, JobSet and LeaderWorkerSet CRDs
and running JobSet/LWS controllers. The fixtures use CPU-only busybox:1.36 Pods.
The repository e2e pins are Kubernetes 1.34.0, JobSet v0.10.1 and LWS v0.7.0.
See `hack/e2e/README.md` for provisioning. This harness installs no controllers.

## Run

Build the assembled implementation, then supply its binary and a disposable
cluster's explicit configuration. The output directory must not already exist.
Run from the repository root:

```sh
(cd exporter && go build -o /tmp/karta-exporter-connected ./cmd)
python3 exporter/test/connected/acceptance.py \
  --binary /tmp/karta-exporter-connected \
  --kubeconfig /path/to/disposable-kubeconfig \
  --context kind-your-disposable-cluster \
  --output /tmp/karta-connected-results
```

The harness copies only the selected context to a private minified kubeconfig.
It never changes the supplied or default kubeconfig. It creates a unique
namespace and one uniquely named cluster Karta. Cleanup deletes only those
resources, stops its exporter process and removes its private kubeconfig.
`--keep-resources` retains the test resources for diagnosis, but still stops the
process and removes the credential copy. The original kubeconfig remains usable.

The runtime contract is `--config=<JSON>`, `--kubeconfig=<file>`,
`--metrics-bind-address=<loopback address>`, `--metrics-exporter=prometheus`,
`--workload-phases=all` and `--identity-config=<JSON>`. Runtime configuration uses
the agreed source/namespace/Pod selector fields. Identity configuration has a
`submittedByExpression` string. The harness restarts the actual process once to
remove the LWS catalog definition. Other definition and object changes happen
while that process remains running.

A newly allocated loopback port keeps runs independent. If another process takes
it before exporter startup, the harness reports the exporter exit. It does not
connect to an unrelated running exporter after that exit.

## Assertions

The harness waits for the real controllers to create three JobSet Pods and six
LWS Pods. It reads their current controller-owner UID chains, including the
leader Pod inside each LWS worker chain. It then checks:

- Exact four-family metric names and complete label sets from the fixed KEP.
  Duplicate samples, duplicate labels and healthy workload-health rows fail.
- Pod/root names and UIDs, API group/version/kind, component, named instance,
  replica group, dense status, provenance, validity and creation time.
- Generic LWS roots when only the intermediate StatefulSet has a Karta.
- Cluster Karta add, same-UID/resourceVersion edit and deletion. The edit changes
  component coordinates and the status phase. Deletion restores generic output.
- Submitter agreement across hidden Pods. A hidden conflicting Pod clears the
  value; removing its conflict restores agreement while its row stays hidden.
- Namespace label removal and restoration, without restarting the exporter.
- Same-name JobSet recreation with new root/Pod UIDs and removal of old series.

Each scenario must pass three consecutive scrapes. The default timeout is 180
seconds per scenario; override it with `--timeout`. A pass checks eventual
convergence, not every transient state or deterministic publication races.
Injected-clock, delayed-callback and pagination-failure tests belong in the
component integration suite. This harness does not claim OTLP, admission-policy,
race-detector or 100k-workload coverage.

`result.json` records the binary hash, server version, scenario outcomes and
cleanup errors. Successful scenario scrapes, exporter logs, commands, live
fixture objects and generated non-secret configuration stay in the output
directory. These files may contain the caller's test-context name and temporary
paths. Review them before publishing. A credential-bearing kubeconfig is never
written to command logs and is removed on normal exit, including test failure.
An externally killed process may leave the private copy for manual cleanup.

A passing baseline package suite is not acceptance of the assembled exporter.
Run this harness only against the binary being evaluated and retain its hash.

## Harness unit checks

These checks validate the parser and independent API-object oracle without any
cluster or exporter process:

```sh
python3 -m unittest discover -s exporter/test/connected -p test_acceptance.py -v
```
