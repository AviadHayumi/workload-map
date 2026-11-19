<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Original jq catalog

These definitions are unchanged copies of `docs/catalog/*.yaml` from
`145d2a7725b7d4c93d2bae95277e653e50ad0ba4`. They are test inputs for the
original jq read expressions and assignable destinations. The SDK does not
load them. Trainer and the new Milvus reference components have no jq baseline.

The separate raw-write replay uses jq to calculate JSON Merge Patch results
at the resolved pointer. That check covers patch behavior and unknown-field
preservation. It does not establish that a translated destination is correct;
the original-path comparisons provide that evidence where jq can identify a path.

One correction is asserted separately: Grove without `podCliqueScalingGroups`
returns an empty set of group values and IDs. The old jq expressions raised
`cannot iterate over: null`. The test requires the groups to be absent and the
CEL result to be empty before allowing this difference.

CronJob replicas and PyTorch worker replicas use computed jq values rather than
assignable paths. Their reads are compared with jq. Their new write destinations
are exercised by the raw-write replay. Status predicates are checked against the
recorded state labels by the existing status replay suite.
