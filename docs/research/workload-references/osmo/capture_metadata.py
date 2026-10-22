# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Keep only reference metadata from a successful namespace-scoped kubectl list."""

import argparse
import datetime
import json
import sys

LABELS = {
    'osmo.workflow_id', 'osmo.workflow_uuid', 'osmo.group_name', 'osmo.group_uuid',
    'osmo.task_name', 'osmo.task_uuid', 'osmo.retry_id', 'osmo.pool', 'osmo.platform',
    'osmo.lead_container', 'research.karta.dev/case',
}
ANNOTATIONS = {'pod-group-name', 'reference.example.test/group-name'}

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--complete-podgroups', action='store_true')
args = parser.parse_args()
source = json.load(sys.stdin)
items = source.get('items', [source])
pods = []
targets = []
for resource in items:
    kind = resource.get('kind')
    if kind not in {'Pod', 'PodGroup'}:
        continue
    original = resource['metadata']
    if original.get('namespace') != 'test-ref':
        raise SystemExit('Capture rejected: object is outside test-ref')
    metadata = {key: original[key] for key in ['namespace', 'name', 'uid']}
    metadata['labels'] = {k: v for k, v in original.get('labels', {}).items() if k in LABELS}
    metadata['annotations'] = {
        k: v for k, v in original.get('annotations', {}).items() if k in ANNOTATIONS
    }
    metadata['ownerReferences'] = original.get('ownerReferences', [])
    retained = {'apiVersion': resource['apiVersion'], 'kind': kind, 'metadata': metadata}
    if kind == 'Pod':
        pods.append(retained)
    else:
        targets.append({
            'namespace': metadata['namespace'], 'id': metadata['name'],
            'attributes': {'name': metadata['name']}, 'kubernetes': retained,
        })
result = {
    'provenance': {
        'kind': 'live_capture',
        'capturedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(),
        'notes': 'Sanitized successful Kubernetes list from test-ref; user identity omitted.',
    },
    'pods': pods,
    'targetSets': {},
}
if args.complete_podgroups:
    result['targetSets']['podgroups'] = {'complete': True, 'targets': targets}
json.dump(result, sys.stdout, indent=2)
sys.stdout.write('\n')
