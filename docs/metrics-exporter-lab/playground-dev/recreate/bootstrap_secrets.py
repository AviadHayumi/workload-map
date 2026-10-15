# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Create missing demo Secrets. Existing Secrets are never replaced."""
import argparse
import base64
import json
import secrets
import subprocess

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--context', required=True)
a = p.parse_args()
k = ['kubectl', '--context', a.context]


def read(namespace, name):
    raw = subprocess.check_output(k + ['-n', namespace, 'get', 'secret', name,
                                      '--ignore-not-found', '-o', 'json'])
    return json.loads(raw) if raw.strip() else None


def create(namespace, name, data, kind='Opaque'):
    if read(namespace, name):
        print(f'Kept existing Secret {namespace}/{name}')
        return
    obj = {'apiVersion': 'v1', 'kind': 'Secret', 'type': kind,
           'metadata': {'name': name, 'namespace': namespace},
           'data': {key: base64.b64encode(value.encode()).decode()
                    for key, value in data.items()}}
    subprocess.run(k + ['create', '-f', '-'], input=json.dumps(obj).encode(),
                   check=True, stdout=subprocess.DEVNULL)
    print(f'Created Secret {namespace}/{name}')


ns = 'karta-metrics-poc'
create(ns, 'karta-monitoring-admin', {'username': 'karta', 'password': secrets.token_urlsafe(32)})
if not read(ns, 'karta-prometheus-auth'):
    admin = read(ns, 'karta-monitoring-admin')['data']
    username = base64.b64decode(admin['username']).decode()
    password = base64.b64decode(admin['password']).decode()
    hashed = subprocess.check_output(['htpasswd', '-niB', username],
                                     input=(password + '\n').encode()).decode()
    create(ns, 'karta-prometheus-auth', {'auth': hashed})
ns = 'karta-metrics-workloads'
create(ns, 'matrix-ngc-placeholder', {'NGC_API_KEY': 'placeholder-unused-by-mock-image'})
create(ns, 'matrix-hf-placeholder', {'HF_TOKEN': 'placeholder-unused-by-mock-image'})
create(ns, 'matrix-pull-placeholder', {'.dockerconfigjson': '{"auths":{}}'},
       'kubernetes.io/dockerconfigjson')
print('Credentials remain in cluster Secrets; no credentials were printed or saved locally.')
