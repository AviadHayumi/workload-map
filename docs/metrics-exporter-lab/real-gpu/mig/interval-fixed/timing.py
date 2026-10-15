# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json, subprocess, time, urllib.request, urllib.parse
from pathlib import Path
out=Path(__file__).parent
k=['kubectl','--kubeconfig',os.environ['KARTA_KUBECONFIG'],'--context',os.environ['KARTA_CONTEXT'],'--request-timeout=20s']
now=time.time(); result={}
for workload,selector,metric,scale in [('real-pytorch-mig-timed','real-pytorch-mig-timed-master-0','DCGM_FI_PROF_GR_ENGINE_ACTIVE',1),('real-full-mig-timed','real-full-mig-timed-.*','DCGM_FI_DEV_GPU_UTIL',.01)]:
 pods=json.loads(subprocess.check_output(k+['get','pods','-n','karta-metrics-workloads','-o','json']))['items']
 pod=next(p['metadata']['name'] for p in pods if p['metadata']['name'].startswith(selector.replace('.*','')))
 log=subprocess.check_output(k+['logs','-n','karta-metrics-workloads',pod],text=True)
 (out/(pod+'.log')).write_text(log)
 events=[json.loads(line) for line in log.splitlines() if line.startswith('{')]
 phases=[x for x in events if x.get('event')=='phase']
 query=metric+'{job="karta-real-dcgm-native",pod=~"'+selector+'"}'
 params={'query':query,'start':phases[0]['time'],'end':now,'step':1}
 response=json.load(urllib.request.urlopen('http://127.0.0.1:19090/karta/prometheus/api/v1/query_range?'+urllib.parse.urlencode(params)))
 raw=response['data']['result'];(out/(workload+'-native-timing.json')).write_text(json.dumps(raw,indent=2)+'\n')
 samples=sorted((float(t),float(v)*scale) for s in raw for t,v in s['values'])
 transitions=[]
 for i,phase in enumerate(phases):
  until=phases[i+1]['time'] if i+1<len(phases) else now
  start=phase['time']; want_active=phase['phase']=='active'
  matches=[t for t,v in samples if start<=t<until and (v>.05 if want_active else v<=.05)]
  first=min(matches,default=None)
  transitions.append({'phase':phase['phase'],'cuda_phase_time':start,'first_native_state_time':first,'observed_delay_seconds':None if first is None else round(first-start,3)})
 result[workload]={'pod':pod,'phases':transitions,'completed':any(x.get('event')=='completed' for x in events),'maximum_transition_delay_seconds':max((x['observed_delay_seconds'] for x in transitions if x['observed_delay_seconds'] is not None),default=None)}
(out/'timing.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result,indent=2))
assert all(x['completed'] and len(x['phases'])==4 and all(t['observed_delay_seconds'] is not None for t in x['phases']) for x in result.values()), 'Finite cycle or telemetry transitions incomplete'
