# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json
import subprocess
import time
import urllib.parse
import urllib.request
from pathlib import Path

OUT = Path(__file__).parent
API = 'http://127.0.0.1:19090/karta/prometheus/api/v1/'
K = ['kubectl', '--kubeconfig', os.environ['KARTA_KUBECONFIG'], '--context', os.environ['KARTA_CONTEXT'], '--request-timeout=20s']
NOW = time.time()

def query(expression, history=False, at=None):
    params = {'query': expression}
    if history:
        params.update(start=NOW-1800, end=NOW, step=10)
    elif at:
        params['time'] = at
    url = API + ('query_range' if history else 'query') + '?' + urllib.parse.urlencode(params)
    return json.load(urllib.request.urlopen(url, timeout=30))['data']['result']

report = {'at': NOW, 'checks': {}, 'workloads': {}}
for kind, name, expected in [('pytorchjob', 'real-pytorch-mig-timed', 2), ('job', 'real-full-mig-timed', 1)]:
    workload = json.loads(subprocess.check_output(K + ['get', kind, name, '-n', 'karta-metrics-workloads', '-o', 'json']))
    (OUT / (name + '.json')).write_text(json.dumps(workload, indent=2)+'\n')
    hist = query('{__name__=~"workload_gpu_observer_(state|low_activity_observation_seconds|utilization_max_ratio|assigned_devices)",workload="'+name+'"}', history=True)
    (OUT / (name+'-history.json')).write_text(json.dumps(hist, indent=2)+'\n')
    def values(metric):
        return {v[0]: float(v[1]) for x in hist if x['metric']['__name__'] == metric for v in x['values']}
    low_seconds = values('workload_gpu_observer_low_activity_observation_seconds')
    activity = values('workload_gpu_observer_utilization_max_ratio')
    devices = values('workload_gpu_observer_assigned_devices')
    lows = [v[0] for x in hist if x['metric'].get('state') == 'low_activity' for v in x['values']]
    actives = [v[0] for x in hist if x['metric'].get('state') == 'active' for v in x['values']]
    states = sorted({x['metric']['state'] for x in hist if x['metric']['__name__'] == 'workload_gpu_observer_state'})
    done = any(c['status'] == 'True' and c['type'] in ['Succeeded', 'Complete'] for c in workload.get('status', {}).get('conditions', []))
    entry = {'uid':workload['metadata']['uid'], 'completed':done, 'states':states,
             'expected_allocations':expected, 'maximum_activity_ratio':max(activity.values(), default=0),
             'minimum_low_evidence_seconds':min((low_seconds.get(t, 0) for t in lows), default=0),
             'low_only_after_full_window':bool(lows) and all(low_seconds.get(t, 0)>=180 and activity.get(t,1)<=.05 and devices.get(t,0)==expected for t in lows),
             'active_when_busy':bool(actives) and all(activity.get(t,0)>.05 for t in actives),
             'resumed_after_low':bool(lows) and any(t>max(lows) for t in actives)}
    report['workloads'][name] = entry
    if name == 'real-pytorch-mig-timed' and lows:
        (OUT / 'verified-time.txt').write_text(str(lows[-1]))
    if name == 'real-pytorch-mig-timed' and actives:
        busy = max(actives, key=lambda t: activity.get(t,0))
        expr = 'DCGM_FI_PROF_GR_ENGINE_ACTIVE{job="karta-real-dcgm-native",pod=~"real-pytorch-mig-timed-.*",GPU_I_ID!="",GPU_CI_ID!=""}'
        raw = query(expr, at=busy)
        report['mig_busy_samples'] = raw
        masters = [float(x['value'][1]) for x in raw if '-master-' in x['metric'].get('pod','')]
        workers = [float(x['value'][1]) for x in raw if '-worker-' in x['metric'].get('pod','')]
        report['checks']['busy_quiet_partition_isolation'] = len(raw)==2 and bool(masters) and bool(workers) and max(masters)>.5 and max(workers)<=.05
        identities = {(x['metric']['UUID'], x['metric']['GPU_I_ID'], x['metric']['GPU_CI_ID']) for x in raw}
        report['checks']['distinct_instances_same_physical_gpu'] = len(identities)==2 and len({x[0] for x in identities})==1
        join = expr+' * on(namespace,pod_uid) group_left(workload,workload_uid,component) karta:pod_attribution:unique'
        joined = query(join, at=busy)
        report['mig_attributed_samples'] = joined
        report['checks']['mig_karta_uid_and_components'] = len(joined)==2 and all(x['metric'].get('workload_uid')==workload['metadata']['uid'] for x in joined) and {x['metric'].get('component') for x in joined}=={'master','worker'}
        report['checks']['mig_has_no_legacy_gpu_util'] = not query('DCGM_FI_DEV_GPU_UTIL{job="karta-real-dcgm-native",GPU_I_ID!=""}', at=busy)
        report['checks']['mig_native_pod_uids'] = len({x['metric'].get('pod_uid') for x in raw})==2 and all(x['metric'].get('pod_uid') for x in raw)
report['checks']['cycles_complete'] = all(x['completed'] for x in report['workloads'].values())
report['checks']['activity_windows_and_recovery'] = all(x['low_only_after_full_window'] and x['active_when_busy'] and x['resumed_after_low'] and {'active','warming','low_activity','no_gpu'}.issubset(x['states']) for x in report['workloads'].values())
report['checks']['all_scrapes_healthy'] = not query('up == 0')
report['checks']['karta_observations_healthy'] = not query('karta_exporter_observation_healthy == 0')
report['checks']['no_ambiguous_pod_uid_mapping'] = not query('count by(namespace,pod_uid)(karta:pod_attribution:unique) > 1')
node = json.loads(subprocess.check_output(K + ['get','node',os.environ['KARTA_GPU_NODE'],'-o','json']))
report['checks']['mixed_capacity'] = node['status']['allocatable'].get('nvidia.com/gpu')=='1' and node['status']['allocatable'].get('nvidia.com/mig-3g.20gb')=='2' and node['metadata']['labels'].get('nvidia.com/mig.config.state')=='success'
report['checks']['worker_ready_and_schedulable'] = not node['spec'].get('unschedulable', False) and any(c['type']=='Ready' and c['status']=='True' for c in node['status']['conditions'])
(OUT/'acceptance.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report,indent=2))
assert all(report['checks'].values()), 'MIG verification has incomplete or failed checks'
