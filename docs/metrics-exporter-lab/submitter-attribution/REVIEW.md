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

Completed using `openai/openai/gpt-6-astra` through NVIDIA Inference, with explicit
user approval for the shareable Markdown. Both completed responses reported that
exact model and `finish_reason=stop`. Each request was a fresh reading without
conversation history, earlier feedback, raw cluster data or credentials.

The reader received README.md, DESIGN.md, OPERATOR-CONTRACT.md,
NVCF-FEATURE-REQUEST.md and EVIDENCE.md. It did not receive the images, raw evidence,
RESULTS.md or FINDINGS.md. Its verdict assesses comprehension of the supplied
proposal, not independent verification of experiments or platform behavior.

| Reading | Result | Author assessment |
| --- | --- | --- |
| [Round 1](reviews/round-1.json) | Completed; revision required for C1 | Accurate teach-back; accepted the missing cross-platform principal namespace rule |
| [Round 2](reviews/round-2.json) | Completed; understandable; no consequential findings | Accurate teach-back of responsibilities, evidence limits, identity scope and service-key example |

C1 identified that a full principal string alone could merge unrelated users or
split one user across platforms. The revised proposal defines the principal key
as `(identity-source, submitter-id)`. The source is an agreed authority/tenant
namespace, with platform/rule provenance kept separately. Cross-source merging
requires a verified mapping; an unknown namespace leaves partial source-local
attribution. These are proposed requirements for platform agreement. This lab
did not verify a fleet identity directory or establish cross-platform mappings.

The final five reviewed Markdown files match the hashes in round-2.json. No
content changes followed that reading. Diagrams and the regenerated PDF were
checked separately by the author; a Markdown review does not inspect images.

The original approval rejection was resolved by explicit user approval. One
initial API attempt returned HTTP 400 because the request omitted the model
field; it produced no review. The corrected request completed as round 1.
Only visible reviewer reports and hashes are published; private model reasoning
and request authentication are excluded.

## Reviewed document hashes

SHA-256 values identify the Markdown used for author verification and PDF input.

| File | SHA-256 |
| --- | --- |
| README.md | `be55a73642da287cdbae6ff36049f55e77567598b73f340434d1b0d1836238d3` |
| DESIGN.md | `f51adab88c534e2c3c017d3cd4b867f2532c614da6093f2189903c1c3587b288` |
| OPERATOR-CONTRACT.md | `934c78d2e999469e41a11bfa1af28e0e968d74a9608e8f455f4df0b792401fb2` |
| NVCF-FEATURE-REQUEST.md | `9470f92d164f74573425450cd72f540830c71738eb915cdb33f09f472fea65b7` |
| RESULTS.md | `7d399ab5bb68111680235e0e2b714772061542f9a2e12178fe8c96775b0ccb72` |
| FINDINGS.md | `a68d5d1371a389ce26ec587ae39ec760e112f0594b4aeec8e345d44f360eadbf` |
| EVIDENCE.md | `4e23c9cf5168c0839ae89053c918afc48ee7c8cdbf050d38add250224a0021b2` |


## Publication checks

`make check` passed in the isolated checkout using the repository-pinned tools.
Generation, manifests, licenses, library tests, CLI tests, CLI lint and stamped
CLI version verification all completed. The tree stayed clean. The earlier publication passed these checks. The documentation review follow-up
reruns the required check before push; its outcome accompanies publication.

The PDF has 13 pages, including one A3 sequence page, and 13 clickable links.
Local document links and the pinned public OSMO source URL resolved. The editable
Excalidraw files include their image assets.

No existing source, API, chart, metric family, running workload, or prior lab
report is changed by this package.
