# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json, subprocess
from pathlib import Path
out=Path(__file__).parent
k=['kubectl','--kubeconfig',os.environ['KARTA_KUBECONFIG'],'--context',os.environ['KARTA_CONTEXT'],'--request-timeout=20s']
p=json.loads(subprocess.check_output(k+['get','clusterpolicies.nvidia.com','cluster-policy','-o','json']))
(out/'dcgm-interval-before.json').write_text(json.dumps(p,indent=2)+'\n')
env=[x for x in p['spec']['dcgmExporter'].get('env',[]) if x['name'] not in ('DCGM_EXPORTER_COLLECT_INTERVAL','DCGM_EXPORTER_INTERVAL')]
env.append({'name':'DCGM_EXPORTER_INTERVAL','value':'5000'})
patch={'spec':{'dcgmExporter':{'env':env}}}
(out/'dcgm-interval-patch.json').write_text(json.dumps(patch,indent=2)+'\n')
subprocess.run(k+['patch','clusterpolicies.nvidia.com','cluster-policy','--type=merge','--patch-file',str(out/'dcgm-interval-patch.json')],check=True)
print('Corrected DCGM_EXPORTER_INTERVAL=5000; other environment settings preserved.')
