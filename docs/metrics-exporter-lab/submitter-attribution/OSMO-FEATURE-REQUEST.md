<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Expose the full authenticated principal to administrator Pod templates for annotations

OSMO already provides useful submitter attribution. The built-in `osmo.submitted_by` label and `WF_SUBMITTED_BY` template variable work when the shortened username is sufficient. Administrators can select an appropriate IdP claim, use guarded templates, or maintain a static username-to-principal mapping. Existing `osmo_tasks_count` metrics also expose the stored `user` and workflow UUID for reporting joins. Workflow/group UUIDs and KAI PodGroups already support grouping.

The remaining need is direct access to the full authenticated workflow principal in administrator templates, so metadata-only consumers can read it from a Pod annotation without a separate API or metric join.

In [6.3.1 label generation](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/src/utils/job/task.py#L2551-L2585), `alice@example.test` becomes `alice`. Another email domain with the same local part produces the same label. The one-character principal `a` produces no submitter label. [Template variables derive from these labels](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/src/utils/job/task.py#L2627-L2648), so copying `WF_SUBMITTED_BY` into an annotation cannot recover the original value.

A live 6.3.1 check covered six Pods across three leader/worker groups. With an administrator mapping, both `alice@example.test` and `alice` produced the configured `alice@example.test` annotation. Karta read that value into `karta_pod_workload_info` and `karta_workload_status`. The missing `a` lookup stayed empty. This confirms the workaround, but a map requires unique lookup keys and maintained identity data. These tests used development authentication; production IdP integration and NVIDIA username claims were not tested.

To repeat the template check, add this to the selected platform, preserving existing settings:

```yaml
default_variables:
  KARTA_USER_BY_SUBMITTER:
    alice:
      principal: alice@example.test
```

Use the existing configuration route: API/CLI in 6.3.1 database mode, or Helm/GitOps in ConfigMap mode; 6.4 is ConfigMap-only. Register this administrator template and append it to the platform's `override_pod_template`, preserving existing entries:

```yaml
metadata:
  annotations:
    workload.dgxc.nvidia.com/submitter-id: >-
      {{KARTA_USER_BY_SUBMITTER.get(
        WF_SUBMITTED_BY | default(''), {}).get('principal', '')}}
```

Save this as `principal-check.yaml`, selecting the configured platform:

```yaml
workflow:
  name: principal-check
  resources:
    default:
      cpu: 1
      memory: 512Mi
      storage: 1Gi
      platform: YOUR_PLATFORM
  groups:
  - name: pair
    tasks:
    - name: leader
      lead: true
      image: ubuntu:24.04
      command: [/bin/sh, -c]
      args: ["sleep 180"]
    - name: worker
      image: ubuntu:24.04
      command: [/bin/sh, -c]
      args: ["sleep 180"]
```

Submit with each test principal's own normal login:

```sh
osmo workflow submit principal-check.yaml --pool YOUR_POOL
```

The lab used development authentication to exercise the three inputs. Keep `a` unmapped; do not treat these lab headers as a production identity check.

Please expose a documented, reserved administrator-template variable containing the stored authenticated principal before label normalization. An opt-in system annotation would also satisfy this need.

Acceptance criteria:

- Preserve the full value, including email domains and one-character identities, consistently across task Pods.
- Define missing-principal behavior so guarded templates remain usable.
- Document service/shared credentials as their credential owner; do not infer a human actor that authentication did not establish.
- Preserve existing label behavior and avoid requiring a metrics or workflow API schema change.

This request complements the merged workflow-label work in [#1221](https://github.com/NVIDIA/OSMO/pull/1221) and configurable prefixes in [#1259](https://github.com/NVIDIA/OSMO/pull/1259). It is scoped to lossless principal access through administrator-controlled metadata.
