# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import argparse
import concurrent.futures
import json
import re
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

parser = argparse.ArgumentParser(description="Verify live Prometheus queries and Grafana time fields without changing the cluster.")
parser.add_argument('--grafana-url', required=True, help='Grafana base URL including its subpath')
parser.add_argument('--namespace', default='karta-metrics-workloads')
parser.add_argument('--workload', default='demo-pytorchjob')
parser.add_argument('--output', type=Path, required=True)
args = parser.parse_args()
if not re.fullmatch(r'[a-z0-9](?:[-a-z0-9]*[a-z0-9])?', args.namespace):
    parser.error('namespace must be a Kubernetes namespace name')
base = Path(__file__).resolve().parents[1]
grafana = args.grafana_url.rstrip('/')
parsed = urllib.parse.urlparse(grafana)
if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username or parsed.password:
    parser.error('Use a Grafana HTTP(S) URL without embedded credentials')
api = grafana + '/api/datasources/proxy/uid/karta-prometheus/api/v1/'

def query(expression, ranged=False):
    values = {'query': expression}
    if ranged:
        now = int(time.time())
        values.update(start=now - 600, end=now, step=30)
    request = urllib.request.Request(api + ('query_range' if ranged else 'query'),
                                     data=urllib.parse.urlencode(values).encode())
    return json.load(urllib.request.urlopen(request, timeout=45))

rows = query('group by(workload,workload_uid,workload_kind)(karta_demo:workload_status{namespace=' + json.dumps(args.namespace) + ',workload=' + json.dumps(args.workload) + '})')['data']['result']
if len(rows) != 1:
    raise SystemExit('Select one existing workload with --namespace and --workload')
steady = rows[0]['metric']
variables = {'namespace': args.namespace, 'workload': '.*', 'workload_uid': steady['workload_uid'],
             'component': '.*', 'component_instance': '.*', 'gpu_device': '.*', 'metric': 'karta_pod_workload_info',
             '__range': '10m', '__rate_interval': '2m', '__interval': '30s'}

def expand(expr):
    def replace(m):
        name = m.group(1) or m.group(2)
        return variables.get(name, m.group(0))
    return re.sub(r'\$\{([A-Za-z_][A-Za-z_0-9]*)(?::[^}]+)?\}|\$([A-Za-z_][A-Za-z_0-9]*)', replace, expr)

def panels(items):
    for item in items:
        yield item
        yield from panels(item.get('panels', []))

bundle = {'data': {name: (base.parent / filename).read_text() for name, filename in
                   {'karta.json':'workloads.json', 'structure.json':'workload-structure.json',
                    'metric-samples.json':'metric-samples.json'}.items()}}
tasks = []
for name, raw in bundle['data'].items():
    doc = json.loads(raw)
    for panel in panels(doc['panels']):
        for target in panel.get('targets', []):
            if target.get('expr'):
                tasks.append({'dashboard': name, 'panel': panel['id'], 'title': panel.get('title'),
                              'ref': target['refId'], 'expr': expand(target['expr']),
                              'range': bool(target.get('range')),
                              'optional_inputs': 'gpu_observer_' in target['expr'] or 'workload_gpu_demo_' in target['expr']})

def check(task):
    try:
        data = query(task['expr'], task['range'])
        values = data.get('data', {}).get('result', [])
        result = {k: v for k, v in task.items() if k != 'expr'}
        result.update(status=data['status'], series=len(values), warnings=data.get('warnings', []))
        if data['status'] != 'success':
            result['error'] = data.get('error')
        return result
    except Exception as exc:
        detail = exc.read().decode()[:1200] if hasattr(exc, 'read') else str(exc)
        return {k: v for k, v in task.items() if k != 'expr'} | {'status': 'error', 'error': detail}

with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
    results = list(pool.map(check, tasks))
frames = []
for name, ids in [('karta.json', [307, 308]), ('structure.json', [9, 10])]:
    doc = json.loads(bundle['data'][name])
    for panel in panels(doc['panels']):
        if panel.get('id') not in ids:
            continue
        target = dict(panel['targets'][0])
        target.update(expr=expand(target['expr']), datasource={'uid':'karta-prometheus','type':'prometheus'},
                      intervalMs=10000, maxDataPoints=1200, format='time_series', range=True, instant=False)
        body = {'queries':[target], 'from':str(int((time.time()-600)*1000)), 'to':str(int(time.time()*1000))}
        request = urllib.request.Request(grafana+'/api/ds/query', data=json.dumps(body).encode(),
                                         headers={'Content-Type':'application/json'})
        data = json.load(urllib.request.urlopen(request,timeout=45))['results']['A']
        entries = data.get('frames',[])
        frames.append({'dashboard':name,'panel':panel['id'],'error':data.get('error'),'frames':len(entries),
                       'time_fields':sum(any(f['type']=='time' for f in x['schema']['fields']) for x in entries)})
record = {'selected_workload':args.workload,'queries':results,'grafana_frames':frames}
args.output.write_text(json.dumps(record,indent=2)+'\n')
errors = [r for r in results if r['status']!='success']
bad_frames = [r for r in frames if r['error'] or not r['frames'] or r['frames']!=r['time_fields']]
print(json.dumps({'queries':len(results),'successful':len(results)-len(errors),
                  'with_data':sum(r.get('series',0)>0 for r in results),'query_errors':errors,
                  'grafana_frames':frames}))
sys.exit(bool(errors or bad_frames))
