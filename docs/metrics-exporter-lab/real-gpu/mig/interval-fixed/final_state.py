# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json,subprocess,urllib.request
from pathlib import Path
out=Path(__file__).parent
accept=json.loads((out/'acceptance.json').read_text())
assert all(accept['checks'].values()),'Do not resume the CronJob before verification passes'
timing=json.loads((out/'timing.json').read_text())
assert all(x['completed'] and len(x['phases'])==4 for x in timing.values())
k=['kubectl','--kubeconfig',os.environ['KARTA_KUBECONFIG'],'--context',os.environ['KARTA_CONTEXT'],'--request-timeout=20s']
subprocess.run(k+['patch','cronjob','real-gpu-cycle','-n','karta-metrics-workloads','--type=merge','-p',json.dumps({'spec':{'suspend':False}})],check=True)
cron=json.loads(subprocess.check_output(k+['get','cronjob','real-gpu-cycle','-n','karta-metrics-workloads','-o','json']))
nodes=json.loads(subprocess.check_output(k+['get','nodes','-o','json']))
service=json.loads(subprocess.check_output(k+['get','services.serving.knative.dev','matrix-knative','-n','karta-metrics-workloads','-o','json']))
host=service['status']['url'].split('/')[2]
req=urllib.request.Request('http://127.0.0.1:18080/',headers={'Host':host})
with urllib.request.urlopen(req,timeout=20) as r:status=r.status;body=r.read().decode()
report={'nodes_ready':sum(any(c['type']=='Ready' and c['status']=='True' for c in n['status']['conditions']) for n in nodes['items']),
'cron_suspended':cron['spec'].get('suspend',False),'cron_schedule':cron['spec']['schedule'],'knative_http_status':status,'knative_body':body,
'knative_ready':any(c['type']=='Ready' and c['status']=='True' for c in service['status']['conditions'])}
(out/'final-state.json').write_text(json.dumps(report,indent=2)+'\n')
(out/'final-nodes.json').write_text(json.dumps(nodes,indent=2)+'\n')
(out/'cron-resumed.json').write_text(json.dumps(cron,indent=2)+'\n')
print(json.dumps(report,indent=2))
assert report['nodes_ready']==6 and not report['cron_suspended'] and status==200 and report['knative_ready']
