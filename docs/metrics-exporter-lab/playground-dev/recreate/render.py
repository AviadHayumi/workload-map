# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Render the saved deployment for another cluster; does not contact Kubernetes."""
import argparse
import json
from pathlib import Path

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--node', required=True, help='One Linux amd64 worker with 32 fake GPUs')
p.add_argument('--storage-class', required=True)
mode = p.add_mutually_exclusive_group(required=True)
mode.add_argument('--local', action='store_true', help='Use localhost port forwards')
mode.add_argument('--host', help='HTTPS hostname, without scheme or path')
p.add_argument('--tls-secret', help='Existing TLS Secret in karta-metrics-poc')
p.add_argument('--monitoring-namespace', default='monitoring')
p.add_argument('--monitoring-release', default='kube-prometheus-stack')
p.add_argument('--output', type=Path, default=Path(__file__).parent / 'rendered')
a = p.parse_args()
if a.host and ('/' in a.host or ':' in a.host):
    p.error('--host must be a hostname without scheme, port, or path')
if a.local and a.tls_secret:
    p.error('--tls-secret is only used with --host')
out = a.output.resolve()
out.mkdir(parents=True, exist_ok=True)
grafana = 'http://127.0.0.1:13000/karta/grafana' if a.local else f'https://{a.host}/karta/grafana'
prometheus = 'http://127.0.0.1:19090/karta/prometheus' if a.local else f'https://{a.host}/karta/prometheus'


def storage(v):
    if isinstance(v, dict):
        for k, item in v.items():
            if k == 'storageClassName':
                v[k] = a.storage_class
            else:
                storage(item)
    elif isinstance(v, list):
        for item in v:
            storage(item)


for path in sorted((Path(__file__).parent / 'assets').glob('*.json')):
    raw = path.read_text().replace('__GPU_NODE__', a.node)
    raw = raw.replace('https://__DEMO_HOST__/karta/grafana', grafana)
    raw = raw.replace('https://__DEMO_HOST__/karta/prometheus', prometheus)
    raw = raw.replace('__DEMO_HOST__', a.host or 'localhost')
    obj = json.loads(raw)
    storage(obj)
    for item in obj['items']:
        if item['kind'] == 'Deployment' and item['metadata']['name'] == 'karta-grafana':
            for env in item['spec']['template']['spec']['containers'][0]['env']:
                if env['name'] == 'GF_SECURITY_COOKIE_SECURE':
                    env['value'] = 'false' if a.local else 'true'
        if item['kind'] == 'ServiceMonitor' and item['metadata']['name'] == 'karta-poc-ksm':
            item['spec']['namespaceSelector']['matchNames'] = [a.monitoring_namespace]
            item['spec']['selector']['matchLabels']['app.kubernetes.io/instance'] = a.monitoring_release
        if item['kind'] == 'Ingress' and a.tls_secret:
            item['spec']['tls'] = [{'hosts': [a.host], 'secretName': a.tls_secret}]
        if item['kind'] == 'ConfigMap' and item['metadata']['name'] == 'karta-grafana-dashboard' and a.local:
            def local_links(v):
                if isinstance(v, dict):
                    return {k: local_links(x) for k, x in v.items()}
                if isinstance(v, list):
                    return [local_links(x) for x in v]
                if isinstance(v, str):
                    if v.startswith('/karta/prometheus'):
                        return 'http://127.0.0.1:19090' + v
                    return v.replace('](/karta/prometheus', '](http://127.0.0.1:19090/karta/prometheus')
                return v
            for key, value in item['data'].items():
                item['data'][key] = json.dumps(local_links(json.loads(value)))
    if path.name == 'ingress.json' and a.local:
        obj['items'] = []
    (out / path.name).write_text(json.dumps(obj, indent=2) + '\n')
    if path.name == 'cronjobs.json':
        for item in obj['items']:
            item['spec']['suspend'] = True
        (out / 'cronjobs-paused.json').write_text(json.dumps(obj, indent=2) + '\n')
(out / 'settings.json').write_text(json.dumps({
    'node': a.node, 'storageClass': a.storage_class,
    'grafana': grafana + '/', 'prometheus': prometheus + '/',
    'mode': 'local' if a.local else 'ingress',
}, indent=2) + '\n')
print(f'Rendered files in {out}')
print(f'Grafana: {grafana}/')
print(f'Prometheus: {prometheus}/')
