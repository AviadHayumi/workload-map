# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Render only the baseline exporter and isolate its lab RBAC aggregation."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

import yaml

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[3]
NAME = 'karta-ref-exporter'
FILES = [
    'serviceaccount.yaml', 'configmap.yaml', 'clusterrole.yaml',
    'clusterrolebinding.yaml', 'service.yaml', 'deployment.yaml',
]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--image', default=os.environ.get(
    'EXPORTER_IMAGE', 'example.invalid/karta-reference-exporter:01352db5'))
args = parser.parse_args()
repository, separator, tag = args.image.rpartition(':')
if not separator or not repository or not tag or '/' in tag:
    raise SystemExit('Provide EXPORTER_IMAGE with an explicit tag or digest')
command = [
    'helm', 'template', 'karta-ref', str(REPO / 'charts/karta'),
    '--namespace', 'test-ref', '--values', str(HERE / 'values.yaml'),
    '--kube-version', '1.35.5',
    '--api-versions', 'admissionregistration.k8s.io/v1/ValidatingAdmissionPolicy',
    '--api-versions', 'admissionregistration.k8s.io/v1/ValidatingAdmissionPolicyBinding',
    '--set-string', f'exporter.image.repository={repository}',
    '--set-string', f'exporter.image.tag={tag}',
]
for filename in FILES:
    command += ['--show-only', f'templates/exporter/{filename}']
result = subprocess.run(command, capture_output=True, text=True, check=True)
objects = [item for item in yaml.safe_load_all(result.stdout) if item]
base = next(item for item in objects if item['kind'] == 'ClusterRole'
            and item['metadata']['name'] == NAME + '-base')
role = next(item for item in objects if item['kind'] == 'ClusterRole'
            and item['metadata']['name'] == NAME)
role.pop('aggregationRule')
role['rules'] = base['rules']
objects.remove(base)
allowed = {'ServiceAccount', 'ConfigMap', 'ClusterRole', 'ClusterRoleBinding',
           'Service', 'Deployment'}
for item in objects:
    if item['kind'] not in allowed or not item['metadata']['name'].startswith('karta-ref-'):
        raise SystemExit('Unexpected resource in exporter render')
    if item['kind'] not in {'ClusterRole', 'ClusterRoleBinding'}:
        if item['metadata'].get('namespace') != 'test-ref':
            raise SystemExit('Unexpected namespace in exporter render')
    if item['kind'] == 'ClusterRole':
        if 'aggregationRule' in item:
            raise SystemExit('Unexpected RBAC aggregation')
        if 'karta.run.ai/exporter-rbac' in item['metadata'].get('labels', {}):
            raise SystemExit('Unexpected shared RBAC aggregation label')
        for rule in item['rules']:
            if rule['verbs'] != ['get', 'list', 'watch']:
                raise SystemExit('Unexpected non-read RBAC rule')
            if '*' in json.dumps(rule) or 'kartas' in rule['resources']:
                raise SystemExit('Unexpected wildcard or cluster Karta read')
print('# SPDX-License-Identifier: Apache-2.0')
print('# Copyright (c) 2026 NVIDIA Corporation')
yaml.safe_dump_all(objects, sys.stdout, sort_keys=False)
