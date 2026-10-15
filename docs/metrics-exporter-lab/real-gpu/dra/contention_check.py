# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json,subprocess,time,urllib.request,urllib.parse
from pathlib import Path
from datetime import datetime
p=Path(__file__).parent
k=['kubectl','--kubeconfig',os.environ['KARTA_KUBECONFIG'],'--context',os.environ['KARTA_CONTEXT'],'--request-timeout=20s','-n','karta-metrics-workloads']
def get(args):return json.loads(subprocess.check_output(k+args+['-o','json']))
def stamp(value):return datetime.fromisoformat(value.replace('Z','+00:00')).timestamp()
def query(q):
 return json.load(urllib.request.urlopen('http://127.0.0.1:19090/karta/prometheus/api/v1/query_range?'+urllib.parse.urlencode({'query':q,'start':time.time()-1200,'end':time.time(),'step':5}),timeout=20))['data']['result']
pods=get(['get','pods']);(p/'cycle-pods-final.json').write_text(json.dumps(pods,indent=2))
full=next(v for v in pods['items'] if v['metadata']['name'].startswith('real-dra-full-cycle-'))
extra=next(v for v in pods['items'] if v['metadata']['name'].startswith('real-dra-contention-probe-'))
job=get(['get','job','real-dra-contention-probe']);(p/'contention-job-final.json').write_text(json.dumps(job,indent=2))
log=subprocess.check_output(k+['logs',extra['metadata']['name']],text=True);(p/'contention.log').write_text(log)
info=json.loads(next(line for line in log.splitlines() if line.startswith('{')))
claims=get(['get','resourceclaims']);(p/'claims-after-cycles.json').write_text(json.dumps(claims,indent=2))
full_finish=stamp(full['status']['containerStatuses'][0]['state']['terminated']['finishedAt'])
extra_start=stamp(extra['status']['containerStatuses'][0]['state']['terminated']['startedAt'])
native=query('DCGM_FI_DEV_GPU_UTIL{job="karta-real-dcgm-native",pod=~"real-dra-contention-probe-.*"}')
states=query('workload_gpu_observer_state{workload="real-dra-contention-probe"}')
(p/'contention-native-history.json').write_text(json.dumps(native,indent=2));(p/'contention-state-history.json').write_text(json.dumps(states,indent=2))
prior=json.loads((p/'acceptance.json').read_text())['full_native_samples'][0]['metric']['UUID']
checks={'initially_pending_without_allocation':json.loads((p/'contention-pending.json').read_text())['claims_unallocated'],'completed':job['status'].get('succeeded')==1,'cuda_exactly_one_device':info['cuda'] and info['device_count']==1,'starts_after_prior_consumer_finishes':extra_start>=full_finish,'native_same_gpu_new_pod_uid':len(native)==1 and native[0]['metric']['UUID']==prior and native[0]['metric']['pod_uid']==extra['metadata']['uid'] and bool(native[0]['metric'].get('dra_claim_name')),'all_generated_claims_removed':not claims['items'],'short_probe_never_classified_low':bool(states) and not any(x['metric']['state']=='low_activity' for x in states)}
result={'checks':checks,'passed':all(checks.values()),'full_cycle_finished':full_finish,'contention_started':extra_start,'states':sorted({x['metric']['state'] for x in states}),'cuda':info}
(p/'contention-final.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2));assert result['passed']
