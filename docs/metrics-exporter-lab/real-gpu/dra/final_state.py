# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json,subprocess,urllib.request,urllib.parse,concurrent.futures,time
from pathlib import Path
out=Path(__file__).parent
assert all(json.loads((out/'acceptance.json').read_text())['checks'].values())
assert json.loads((out/'contention-final.json').read_text())['passed']
assert all(x['completed'] and len(x['phases'])==4 for x in json.loads((out/'timing.json').read_text()).values())
k=['kubectl','--kubeconfig',os.environ['KARTA_KUBECONFIG'],'--context',os.environ['KARTA_CONTEXT'],'--request-timeout=20s']
def get(item):
 name,args=item;v=json.loads(subprocess.check_output(k+args+['-o','json']));(out/name).write_text(json.dumps(v,indent=2));return name,v
values=dict(concurrent.futures.ThreadPoolExecutor().map(get,[('final-nodes.json',['get','nodes']),('final-policy.json',['get','clusterpolicies.nvidia.com','cluster-policy']),('final-gpu-pods.json',['get','pods','-n','gpu-operator']),('final-slices.json',['get','resourceslices']),('final-claims.json',['get','resourceclaims','-A']),('final-knative.json',['get','services.serving.knative.dev','matrix-knative','-n','karta-metrics-workloads'])]))
nodes=values['final-nodes.json']['items'];node=next(x for x in nodes if x['metadata']['name']==os.environ['KARTA_GPU_NODE']);policy=values['final-policy.json'];pods=values['final-gpu-pods.json']['items']
def ready(p):return any(c['type']=='Ready' and c['status']=='True' for c in p['status'].get('conditions',[]))
checks={'six_nodes_ready':len(nodes)==6 and all(ready(n) for n in nodes),'worker_schedulable':not node['spec'].get('unschedulable',False),'original_capacity':node['status']['allocatable'].get('nvidia.com/gpu')=='1' and node['status']['allocatable'].get('nvidia.com/mig-3g.20gb')=='2','mig_geometry_healthy':node['metadata']['labels'].get('nvidia.com/mig.config.state')=='success','device_plugin_enabled':policy['spec']['devicePlugin']['enabled'],'device_plugin_ready':any(x['metadata']['name'].startswith('nvidia-device-plugin') and ready(x) for x in pods),'dcgm_ready':any(x['metadata']['name'].startswith('nvidia-dcgm-exporter') and ready(x) for x in pods),'dra_plugin_gone':not any(x['metadata']['name'].startswith('karta-dra-validation-kubelet-plugin') for x in pods),'no_test_slices':not values['final-slices.json']['items'],'no_claims':not values['final-claims.json']['items'],'dcgm_env_restored':policy['spec']['dcgmExporter'].get('env',[])==json.loads((out/'nvidia-policy-before.json').read_text())['spec']['dcgmExporter'].get('env',[])}
q='up == 0';bad=json.load(urllib.request.urlopen('http://127.0.0.1:19090/karta/prometheus/api/v1/query?'+urllib.parse.urlencode({'query':q}),timeout=20))['data']['result'];checks['all_scrapes_healthy']=not bad
svc=values['final-knative.json'];host=svc['status']['url'].split('/')[2]
with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:18080/',headers={'Host':host}),timeout=20) as response: status=response.status;body=response.read().decode()
checks['knative_healthy']=ready(svc) and status==200
report={'at':time.time(),'checks':checks,'knative_http_status':status,'knative_body':body,'failed_scrapes':bad}
(out/'restore-health.json').write_text(json.dumps(report,indent=2));print(json.dumps(report,indent=2),flush=True);assert all(checks.values()),'Do not resume CronJob before restoration is healthy'
subprocess.run(k+['patch','cronjob','real-gpu-cycle','-n','karta-metrics-workloads','--type=merge','-p',json.dumps({'spec':{'suspend':False}})],check=True)
cron=json.loads(subprocess.check_output(k+['get','cronjob','real-gpu-cycle','-n','karta-metrics-workloads','-o','json']));(out/'cron-resumed.json').write_text(json.dumps(cron,indent=2))
report['cron_suspended']=cron['spec'].get('suspend',False);report['cron_schedule']=cron['spec']['schedule'];report['passed']=all(checks.values()) and not report['cron_suspended'];(out/'final-state.json').write_text(json.dumps(report,indent=2));print(json.dumps({'passed':report['passed'],'cron_suspended':report['cron_suspended']},indent=2));assert report['passed']
