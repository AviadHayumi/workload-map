#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Run with local images built from the legacy revision and the current tree.
# Creates its own kind cluster. Karta CRDs are replaced only inside this cluster.
set -euo pipefail
: "${LEGACY_IMAGE:?set to the local operator image built from the jq revision}"
: "${CEL_IMAGE:?set to the local operator image built from this checkout}"
base=145d2a7725b7d4c93d2bae95277e653e50ad0ba4
repo=$(git rev-parse --show-toplevel)
cd "$repo"
tmp=$(mktemp -d)
cluster="karta-migration-${tmp##*.}"
cluster=$(tr '[:upper:]' '[:lower:]' <<< "$cluster")
export KUBECONFIG="$tmp/kubeconfig"
mkdir "$tmp/legacy"
git archive "$base" charts/karta docs/catalog | tar -x -C "$tmp/legacy"
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --image "${KIND_NODE_IMAGE:-kindest/node:v1.34.0}" --wait 120s
trap 'kind delete cluster --name "$cluster"; rm -rf "$tmp"' EXIT
kind load docker-image --name "$cluster" "$LEGACY_IMAGE" "$CEL_IMAGE"
k() { kubectl --context "kind-$cluster" "$@"; }
chart() {
    local path=$1 image=$2
    shift 2
    helm upgrade --install test "$path" --kube-context "kind-$cluster" --namespace test --create-namespace --wait --timeout 3m \
        --set crdUpgrader.enabled=false --set "image.repository=${image%:*}" --set "image.tag=${image##*:}" \
        --set image.pullPolicy=Never "$@"
}
stop_release() { helm uninstall test --kube-context "kind-$cluster" --namespace test --wait; }

chart "$tmp/legacy/charts/karta" "$LEGACY_IMAGE"
k create -f "$tmp/legacy/docs/catalog/batch-job-v1.yaml"
# Back up the stored definitions independently of the release artifact. Retain
# user metadata and remove API-server bookkeeping before restoring on rollback.
k get kartas -o json | jq '.items[] | del(.status,.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp,.metadata.generation,.metadata.managedFields)' > "$tmp/backup.json"
k get crd kartas.run.ai -o json | jq '{apiVersion,kind,metadata:{name:.metadata.name},spec}' > "$tmp/legacy-crd.json"
k create job unchanged --image=registry.k8s.io/pause:3.10
uid=$(k get job unchanged -o jsonpath='{.metadata.uid}')
stop_release
k delete crd kartas.run.ai --wait=true
chart charts/karta "$CEL_IMAGE" --set catalogInstaller.enabled=true
catalog=(docs/catalog/*.yaml)
k get kartas -o json | jq -e --argjson count "${#catalog[@]}" \
    '(.items | length) == $count and all(.items[]; .apiVersion=="run.ai/v1alpha2" and .metadata.annotations["karta.run.ai/catalog-owner"]=="test/test")' >/dev/null
k get crd kartas.run.ai -o json | jq -e '.status.storedVersions==["v1alpha2"]' >/dev/null
# Simulate a partial installation. A retry must restore the missing definition
# and preserve the identities of the definitions already installed.
survivor=$(k get karta batch-job-v1 -o jsonpath='{.metadata.uid}')
k delete karta apps-deployment-v1
chart charts/karta "$CEL_IMAGE" --set catalogInstaller.enabled=true
k get karta apps-deployment-v1 >/dev/null
test "$survivor" = "$(k get karta batch-job-v1 -o jsonpath='{.metadata.uid}')"
stop_release
k delete crd kartas.run.ai --wait=true
k create -f "$tmp/legacy-crd.json"
k wait --for condition=Established crd/kartas.run.ai
chart "$tmp/legacy/charts/karta" "$LEGACY_IMAGE"
k create -f "$tmp/backup.json"
k get karta batch-job-v1 -o json | jq -e --slurpfile old "$tmp/backup.json" '.apiVersion=="run.ai/v1alpha1" and .spec==$old[0].spec' >/dev/null
test "$uid" = "$(k get job unchanged -o jsonpath='{.metadata.uid}')"
echo 'Verified jq -> CEL -> jq, full catalog, partial retry and workload identity.'
