<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Native submitter admission tests

The test renders the real Helm chart and installs its policies into a new local
API server and etcd. It never reads an existing kubeconfig. Helm and local
envtest binaries are required. Set `KARTA_ADMISSION_ENVTEST_ASSETS` to the binary
directory, then run from `operator/`:

```sh
go test ./test/admission -count=1 -v
```

Use Kubernetes 1.36+ for stable MAP v1. For 1.34-1.35, also set
`KARTA_ADMISSION_ENVTEST_BETA=true`. The test enables the MAP feature gate and
`admissionregistration.k8s.io/v1beta1` API in its private server.

Coverage includes authenticated CREATE, missing and supplied annotations,
service-account principal strings, namespace/resource exclusions and conditions,
ordinary updates, and identity change/removal after capture scope narrows or
capture is disabled. Identity comes from API-server impersonation of test
principals. No service-account token issuance is tested.

Pod `/status` permits metadata updates. Tests require unrelated annotation
updates to succeed while submitter changes fail. CRD `/status` restores old
metadata before validation, so it cannot demonstrate this protection. A test
CRD separately verifies capture of custom resources.

The chart also retains UPDATE protection across Helm uninstall with
`helm.sh/resource-policy: keep`. Rendering tests check this annotation; this
suite does not execute a Helm uninstall.
