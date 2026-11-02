# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Render local setup files. This script never contacts or changes a cluster."""
import argparse
import json
from pathlib import Path
from urllib.parse import urlparse

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--grafana-host', required=True)
parser.add_argument('--prometheus-url', required=True)
parser.add_argument('--storage-class', required=True)
parser.add_argument('--ingress-class', default='nginx')
parser.add_argument('--tls-secret', required=True)
parser.add_argument('--output-dir', type=Path, required=True)
args = parser.parse_args()
url = urlparse(args.prometheus_url)
if url.scheme not in ('http', 'https') or not url.hostname or url.username or url.password or url.query:
    parser.error('Use a Prometheus HTTP(S) service URL without embedded credentials or query parameters')
if any(c in args.grafana_host for c in '/:@'):
    parser.error('grafana-host must be a hostname without scheme, credentials or port')
base = Path(__file__).resolve().parents[1]
args.output_dir.mkdir(parents=True, exist_ok=True)
replacements = {'__GRAFANA_HOST__': args.grafana_host, '__PROMETHEUS_URL__': args.prometheus_url,
                '__STORAGE_CLASS__': args.storage_class, '__INGRESS_CLASS__': args.ingress_class,
                '__TLS_SECRET_NAME__': args.tls_secret}

def render(value):
    if isinstance(value, dict):
        return {k: render(v) for k, v in value.items()}
    if isinstance(value, list):
        return [render(v) for v in value]
    if isinstance(value, str):
        for key, replacement in replacements.items():
            value = value.replace(key, replacement)
    return value

grafana = render(json.loads((base / 'manifests/grafana-template.json').read_text()))
(args.output_dir / 'grafana.json').write_text(json.dumps(grafana, indent=2) + '\n')
files = {'karta.json': 'workloads.json', 'structure.json': 'workload-structure.json',
         'metric-samples.json': 'metric-samples.json'}
dashboards = {key: json.dumps(json.loads((base.parent / name).read_text()), separators=(',', ':'))
              for key, name in files.items()}
bundle = {'apiVersion': 'v1', 'kind': 'ConfigMap',
          'metadata': {'name': 'karta-grafana-dashboard', 'namespace': 'monitoring'}, 'data': dashboards}
(args.output_dir / 'dashboard.json').write_text(json.dumps(bundle, indent=2) + '\n')
print('Rendered grafana.json and dashboard.json; inspect them before applying.')
