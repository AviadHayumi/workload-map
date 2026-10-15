# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json,subprocess,time,urllib.request,urllib.parse
from pathlib import Path
p=Path(__file__).parent
k=['kubectl','--kubeconfig',os.environ['KARTA_KUBECONFIG'],'--context',os.environ['KARTA_CONTEXT'],'--request-timeout=20s','-n','karta-metrics-workloads']
report={}
for name in ['real-restored-full-probe','real-restored-mig-probe']:
 job=json.loads(subprocess.check_output(k+['get','job',name,'-o','json']));(p/(name+'.json')).write_text(json.dumps(job,indent=2))
 log=subprocess.check_output(k+['logs','job/'+name],text=True);(p/(name+'.log')).write_text(log);info=json.loads(next(l for l in log.splitlines() if l.startswith('{')))
 q='{__name__=~"workload_gpu_observer_(state|assigned_devices)",workload="'+name+'"}'
 history=json.load(urllib.request.urlopen('http://127.0.0.1:19090/karta/prometheus/api/v1/query_range?'+urllib.parse.urlencode({'query':q,'start':time.time()-600,'end':time.time(),'step':5}),timeout=20))['data']['result'];(p/(name+'-history.json')).write_text(json.dumps(history,indent=2))
 counts=[float(v) for s in history if s['metric']['__name__']=='workload_gpu_observer_assigned_devices' for _,v in s['values']]
 states={s['metric']['state'] for s in history if s['metric']['__name__']=='workload_gpu_observer_state'}
 report[name]={'completed':job['status'].get('succeeded')==1,'cuda':info,'observer_verified_one_device':bool(counts) and all(v==1 for v in counts),'states':sorted(states),'never_false_low':'low_activity' not in states}
passed=all(v['completed'] and v['cuda']['cuda'] and v['cuda']['device_count']==1 and v['observer_verified_one_device'] and v['never_false_low'] for v in report.values())
result={'passed':passed,'workloads':report};(p/'restored-probe-results.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2));assert passed
