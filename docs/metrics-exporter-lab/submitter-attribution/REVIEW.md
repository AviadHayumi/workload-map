<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Documentation validation record

16 October 2026. Documentation-only addition on repository base `5d0661ab`.

## Author verification

- Checked existing SDK symbols and owner-index behavior against the base source.
- Compiled and ran the exact Go example with a recognized Pod and an unsupported
  kind; both passed. Unsupported objects retained ordinary metadata access.
- Checked local Markdown links and parsed every included evidence JSON file.
- Rendered the PDF and inspected every page, plus the original diagram PNGs.
- Kept live product observations separate from source inspection and proposals.
- The PromQL recipe was source-checked; it was not run against lab Prometheus.
- CEL extraction and canonical admission stamping were not deployed or tested.
- Scanned the publication for credentials, private links and local infrastructure
  addresses. Selected evidence omits installation configuration.

## Independent comprehension review

Not completed. A stateless review of the report through NVIDIA Inference was
blocked by automatic approval review because it included unpublished lab findings.
Explicit approval for that payload was requested and had not arrived when this
record was prepared. No external-review pass is claimed. Author verification
and visual inspection are separate from an independent reader teach-back.

## Reviewed document hashes

SHA-256 values identify the Markdown used for author verification and PDF input.

| File | SHA-256 |
| --- | --- |
| README.md | `14e0d0a9fc63aba52dee9cd4893aa3a26b4d12d6111b81188888439e665a61f1` |
| DESIGN.md | `f59c48d24a672983007285eb8d25635438b355b18f16c0243ae175960f9e5356` |
| OPERATOR-CONTRACT.md | `a18f662856e767d1c720a92b14621bc3f9308852b20b4a6905d6227ad4e42990` |
| NVCF-FEATURE-REQUEST.md | `a55c33f86e5e0ceb35b62cb59f1b5f286a6ef0be5e03081ed882b3c9b0436617` |
| RESULTS.md | `7d399ab5bb68111680235e0e2b714772061542f9a2e12178fe8c96775b0ccb72` |
| FINDINGS.md | `a68d5d1371a389ce26ec587ae39ec760e112f0594b4aeec8e345d44f360eadbf` |
| EVIDENCE.md | `e9587aea70d6b002d7c9c01b8fae13363d3cd4394660c91897678b8a493307d4` |


## Publication checks

The repository-required `make check` is run in an isolated checkout before push.
Its outcome is reported with the publication. No existing source, API, chart,
metric family, running workload, or prior lab report is changed by this package.
