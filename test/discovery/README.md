<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Live discovery acceptance tests

The `live_discovery` build tag runs public SDK checks against a dedicated kind
cluster. An independent Python oracle reads fixture identities and upstream
operator labels, then compares exact root and Pod UID sets against default direct,
opt-in owner metadata, JSON Pod reads and indexed discovery with and without
attribution reuse. It imports no Karta code and does not
derive expectations from the resolver under test.

The reproducible fixture contains seven roots and fourteen Pods:

| Root | Expected members | Pods |
| --- | --- | --- |
| JobSet/sdk-jobset | chief replica 0; workers replicas 0 and 1 | 3 |
| LeaderWorkerSet/sdk-lws | groups 0 and 1, each with one leader and one worker | 4 |
| Deployment/sdk-deployment | Two owned Pods | 2 |
| Bundle/sdk-bundle | Pods from both sdk-generic-a and sdk-generic-b Deployments | 3 |
| Pod/sdk-standalone | The root Pod itself | 1 |
| Deployment/sdk-zero | Empty workload | 0 |
| Job/sdk-finite | One completed Pod | 1 |

JobSet's `replicatedjob` component has instances `chief` and `workers`; job-index
labels identify their replica keys. LWS's `group` component has anonymous instance
`""` and replica keys `0` and `1`. Each replica contains `leader` and `worker`
components with anonymous instances. The oracle cross-checks explicit LWS Pod
names and JobSet Job ownership. Physical Pods and StatefulSets are not relabeled
as semantic component instances.

The membership suite checks forty-two workload subtests: seven roots in each
of four direct reader configurations, indexed mode, and indexed mode with
attribution reuse. The cache-enabled mode also repeats full-object reads. It also requires the
metadata route to perform owner reads against the live API. Separate ownership
churn checks passed for reparenting, immutable prior observations, terminating and
unscheduled Pods, RBAC denial, owner-name reuse, rejected retired UIDs, and
invalidation. A cluster-scoped root also passed exact membership across two
namespaces, and a namespace-only index rejected that query as incomplete coverage.
These are correctness checks, not performance claims.

Each mode also checks the public `OwnershipPath` and `PodMemberships` APIs:

- Paths include every controller owner, including intermediate Jobs and ReplicaSets.
- Every edge matches the independently read controller reference and UID.
- Memberships cover the exact Pod UID set.
- JobSet instance/replica values and LWS group/role values match the operator-label oracle.
- Root-based, Pod-based and repeated cached queries agree.

The churn test retries only an initial `ErrStaleInput` for up to ten seconds.
A new Pod can change between GET and LIST before the test begins its own mutations.
Other errors fail immediately; production consistency checks are unchanged.

## Exercise real consumer flows

Three additional E2E tests cover the SDK examples against running controllers:

| Test | What must happen |
| --- | --- |
| [Job lifecycle](consumer_e2e_test.go) | Persist Suspend, edit a suspended template, Resume, then verify Karta lifecycle phases and Kubernetes status. The actual Pod must inherit the template label. |
| [JobSet scopes](consumer_e2e_test.go) | Persist a workers-only template label. Chief stays unchanged. Return exact full Pods by component, named instance, replica and scheduling gang. |
| [LeaderWorkerSet gangs](scheduling_groups_test.go) | Each group contains its actual leader and worker. Groups never cross replica boundaries or lose full Pod fields. |

Each flow runs through direct discovery, an index and an index with attribution reuse.
The lifecycle tests start from both the root and its actual Pods.
They retain earlier observations and verify that later API changes leave them intact.

Expected full Pod bodies come from a separate dynamic-client List and independent
conversion to Kubernetes Pod types. The SDK reader is not its own body oracle.
LWS membership also uses the independent fixture UID oracle above.

The Job test checks Karta phases `Suspended`, `Running` and `Completed` in
`DesiredTree(ctx).Status.Phases`. It separately verifies that
`RootStatus.Conditions` preserves the controller's raw conditions.
The API waits require Job `Suspended=True`, then `status.ready=1`, then `Complete=True`.
The actual Pod moves from Kubernetes phase `Running` to `Succeeded`.

The Job waits for an explicit completion signal inside its container.
The test releases it with `kubectl exec` after checking the running state, so timing
alone cannot skip it. Controller status is never fabricated.
Mutation tests create temporary namespaces and register cleanup immediately.

JobSet v0.10.1 permits selected Pod-template scheduling changes while suspended.
The test persists a label edit before the first start, then checks that only
workers inherit it. This does not imply arbitrary template or instance-name edits
are accepted. See the [upstream update validation](https://github.com/kubernetes-sigs/jobset/blob/v0.10.1/pkg/webhooks/jobset_webhook.go#L285-L303).

After preparing the cluster and oracle below, run just these flows:

```bash
KARTA_DISCOVERY_MUTATION_TESTS=true \
  go test -race -tags live_discovery ./test/discovery -count=1 -v \
  -run '^(TestLiveConsumerJobLifecycle|TestLiveConsumerJobSetScopes|TestLiveLeaderWorkerSchedulingGroups)$'
```

## Update definitions without rebuilding the SDK

[Provider updates](provider_updates_test.go) reads the existing JobSet and its
three actual Pods. It changes only an in-process definition registry:

1. Observe the selected catalog definition.
2. Publish an empty selection. The same workload becomes generic.
3. Publish a revised component name. New observations use that name.
4. Restore the original definition. Classified memberships match the first result.
5. Return a provider error. The query fails instead of reporting a generic workload.

Direct, indexed and cached queries must preserve the exact root and full Pod bodies.
Each query starts from both the root and its actual Pods.
Earlier observations must stay unchanged after every publication.
The test makes no Kubernetes writes and does not require the mutation switch.

```bash
go test -race -tags live_discovery ./test/discovery -count=1 -v \
  -run '^TestLiveDefinitionProviderUpdates$'
```

The registry does not watch Karta objects itself.
The [operator example](../../examples/workloadinterface/OPERATORS.md) shows how a
consumer publishes its selected definitions and controls readiness.

## Prepare the dedicated cluster

Run from the repository root. Prerequisites are Docker, kind, kubectl, Python 3.9
or newer, and the Go version in `go.mod`. The existing operator installers use the
version defaults in `hack/e2e/global.env`; this reproduction explicitly selects
JobSet v0.10.1 and LWS v0.7.0. No Karta operator installation is required.

Keep kubeconfigs, oracle output, and logs in ignored local output. The local Git
exclude applies to this checkout without changing tracked repository settings:

```bash
mkdir -p .context/discovery-live
printf '%s\n' '.context/' >> "$(git rev-parse --git-path info/exclude)"
export KARTA_DISCOVERY_KUBECONFIG="$PWD/.context/discovery-live/kubeconfig"
export KARTA_DISCOVERY_CONTEXT="kind-karta-sdk-discovery"
export KARTA_DISCOVERY_ORACLE="$PWD/.context/discovery-live/expected.json"

kind create cluster --name karta-sdk-discovery \
  --image kindest/node:v1.34.0 \
  --config test/discovery/fixtures/config.yaml \
  --kubeconfig "$KARTA_DISCOVERY_KUBECONFIG"
```

To reuse this dedicated cluster, export its kubeconfig instead of creating it:

```bash
kind export kubeconfig --name karta-sdk-discovery \
  --kubeconfig "$KARTA_DISCOVERY_KUBECONFIG"
```

Both Python scripts require `KARTA_DISCOVERY_KUBECONFIG`. They use
`KARTA_DISCOVERY_CONTEXT`, defaulting to `kind-karta-sdk-discovery`, and reject
contexts whose names do not begin with `kind-`. Every kubectl request carries the
explicit kubeconfig and context. The scripts do not read or change the global
kubeconfig.

The existing install scripts invoke plain kubectl, so run them with the dedicated
kubeconfig selected in a subshell. The context change below affects only that
file. The scripts apply version-pinned upstream manifests and wait for their
controller deployments:

```bash
(
  export KUBECONFIG="$KARTA_DISCOVERY_KUBECONFIG"
  case "$KARTA_DISCOVERY_CONTEXT" in
    kind-?*) ;;
    *) echo 'A dedicated kind context is required' >&2; exit 1 ;;
  esac
  kubectl --kubeconfig "$KARTA_DISCOVERY_KUBECONFIG" \
    config use-context "$KARTA_DISCOVERY_CONTEXT"
  JOBSET_VERSION=v0.10.1 bash hack/e2e/operators/jobset/install.sh
  LWS_VERSION=v0.7.0 bash hack/e2e/operators/lws/install.sh
)
```

Create the fixtures and their current identity oracle:

```bash
python3 test/discovery/fixtures/setup-fixtures.py
python3 test/discovery/fixtures/build-oracle.py
```

Setup waits for both operator CRDs to be Established and both controller
deployments to finish rollout. It creates the Bundle CRD, waits for Established,
then creates its instance and assigns its current UID to the two child
Deployments. It also waits for all expected long-running Pods to be ready and for
the finite Job to complete. A completed finite Job is reused on repeated runs.

The fixture namespace is `sdk-discovery-test`. Use it only for this fixture set:
the oracle requires its fourteen Pods to form an exact, disjoint partition of the
seven roots. The pinned fixture also checks 32 observed API objects. Unexpected
Pods, restarts that recreate completed Jobs, or additional controller revisions
cause the oracle to fail instead of updating expectations silently. Long-running
fixture containers sleep for one hour; recreate expired fixtures when necessary.

`KARTA_DISCOVERY_ORACLE` must name an ignored file under this checkout's
`.context/`. The oracle captures fresh UIDs and owner references there. Regenerate
it after recreating any fixture. Never copy its contents, kubeconfigs, or observed
cluster objects into tracked fixtures. The checked-in YAML contains no recorded
UIDs or credentials.

## Run the checks

The membership suite reads the existing fixtures and compares their exact UID
sets. It checks both root-to-Pods and Pod-to-whole-workload discovery, generic
Bundle behavior, JobSet instance/replica scopes, and retained LWS group scopes:

```bash
go test -tags live_discovery ./test/discovery \
  -run '^TestLiveOperatorMembership$' -count=1 -v
```

The mutation suite requires an additional explicit environment switch. It creates
isolated resources and cleans them up. It exercises reparenting, UID reuse,
termination, authorization failure, and index invalidation, including warmed
attribution entries before ownership changes. The cluster-root test
creates a temporary cluster-scoped CRD and Pods in two namespaces, checks
all-namespace membership, and rejects a namespace-only index. A separate suspended
JobSet checks named-instance scopes, a metadata update with a forced API conflict,
and replacement by a new UID. Its instance names are not changed in place;
unit tests cover instance declaration changes under the same UID:

```bash
KARTA_DISCOVERY_MUTATION_TESTS=true \
  go test -tags live_discovery ./test/discovery \
  -run '^(TestLiveOwnershipChurn|TestLiveClusterRoot|TestLiveJobSetNamedInstanceCache)$' -count=1 -v
```

Run all live checks together with:

```bash
KARTA_DISCOVERY_MUTATION_TESTS=true \
  go test -tags live_discovery ./test/discovery -count=1 -v
```

An oracle error should be investigated before regeneration. A discovery failure
must not be resolved by dropping completed, unscheduled, or generic workloads
from the expected sets.

The live workload test also acquires observations through `karta.Discoverer` and
runs the [README consumer adapters](../../examples/workloadinterface/README.md).
It checks observed Pod totals, completed Job counting, root-inclusive desired
templates and isolation of local template edits from live observations.

The regular SDK tests use fake readers and explicit expected membership sets.
They do not require a cluster:

```bash
go test -race ./pkg/ownership ./pkg/attribution ./pkg/discovery/...
```

## Remove the dedicated cluster

This removes the dedicated test cluster and all fixtures in it:

```bash
kind delete cluster --name karta-sdk-discovery
```

The ignored local kubeconfig and oracle may then be removed. The public SDK
contract and examples are in [Live Workload Discovery](../../docs/Live%20Workload%20Discovery.md).

The mutation suite also creates a dedicated suspended JobSet to verify repeated
named-instance scopes, a published metadata update, and delete/recreate with a
new UID and different instance name. Instance-name changes and internal
cache-hit reuse are unit-tested. The separate consumer E2E test persists a
permitted workers-only template-label edit while suspended. Neither test
weakens admission or changes the shared operator fixtures.
