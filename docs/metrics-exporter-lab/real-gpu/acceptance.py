# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json,pathlib,subprocess,time,urllib.request,urllib.parse
b=pathlib.Path(__file__).resolve().parent
base='http://127.0.0.1:19090/karta/prometheus/api/v1/'
k=['kubectl','--kubeconfig',os.environ['KARTA_KUBECONFIG'],'--context',os.environ['KARTA_CONTEXT'],'--request-timeout=20s']
def query(q,range=False):
 params={'query':q}
 if range:params.update(start=time.time()-2400,end=time.time(),step=10)
 return json.load(urllib.request.urlopen(base+('query_range' if range else 'query')+'?'+urllib.parse.urlencode(params),timeout=30))['data']['result']
report={'at':time.time(),'checks':{},'workloads':{}}
for name in ['real-pytorch-cycle','real-pytorch-verified']:
 status=json.loads(subprocess.check_output(k+['get','pytorchjob',name,'-n','karta-metrics-workloads','-o','json']))
 hist=query('{__name__=~"workload_gpu_observer_(state|low_activity_observation_seconds|utilization_max_ratio|assigned_devices)",workload="'+name+'"}',True)
 (b/(name+'-history.json')).write_text(json.dumps(hist,indent=2))
 states={x['metric']['state'] for x in hist if x['metric']['__name__']=='workload_gpu_observer_state'}
 def bytime(metric):return {v[0]:float(v[1]) for x in hist if x['metric']['__name__']==metric for v in x['values']}
 low_seconds=bytime('workload_gpu_observer_low_activity_observation_seconds');util=bytime('workload_gpu_observer_utilization_max_ratio');devices=bytime('workload_gpu_observer_assigned_devices')
 low_times=[v[0] for x in hist if x['metric'].get('state')=='low_activity' for v in x['values']]
 active_times=[v[0] for x in hist if x['metric'].get('state')=='active' for v in x['values']]
 completed=any(x['type']=='Succeeded' and x['status']=='True' for x in status.get('status',{}).get('conditions',[]))
 result={'uid':status['metadata']['uid'],'completed':completed,'states':sorted(states),'low_activity_samples':len(low_times),'minimum_low_evidence_seconds':min((low_seconds.get(t,0) for t in low_times),default=0),'maximum_utilization_ratio':max(util.values(),default=0),'low_only_after_180_seconds':bool(low_times) and all(low_seconds.get(t,0)>=180 and util.get(t,1)<=.05 and devices.get(t,0)==2 for t in low_times),'active_when_busy':bool(active_times) and all(util.get(t,0)>.05 for t in active_times)}
 report['workloads'][name]=result
report['checks']['all_scrapes_up']=not query('up == 0')
report['checks']['workload_observations_healthy']=not query('karta_exporter_observation_healthy{scope="workload"} == 0')
report['checks']['resource_type_observations_healthy']=not query('karta_exporter_observation_healthy{scope="resource_type"} == 0')
report['checks']['no_ambiguous_pod_mapping']=not query('count by(namespace,pod_uid)(karta:pod_attribution:unique) > 1')
report['checks']['native_gpu_devices']=len(query('DCGM_FI_DEV_GPU_UTIL'))==2
report['checks']['hardware_dcgm_provenance']=all(x['metric'].get('source')=='dcgm' and 'A100' in x['metric'].get('modelName','') for x in query('DCGM_FI_DEV_GPU_UTIL'))
report['checks']['all_cycles_pass']=all(x['completed'] and x['low_only_after_180_seconds'] and x['active_when_busy'] and {'active','warming','low_activity','no_gpu'}.issubset(x['states']) for x in report['workloads'].values())
(b/'acceptance.json').write_text(json.dumps(report,indent=2));print(json.dumps(report,indent=2));assert all(report['checks'].values()),'Some acceptance checks are incomplete or failed'
