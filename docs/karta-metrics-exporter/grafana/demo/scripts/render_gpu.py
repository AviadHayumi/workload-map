# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import argparse
import json
from pathlib import Path
parser=argparse.ArgumentParser(description="Render the GPU demo resources without changing a cluster.")
parser.add_argument("--nodes-file",type=Path,required=True,help="JSON array of existing fake GPU node names")
parser.add_argument("--output",type=Path,required=True)
args=parser.parse_args()
nodes=json.loads(args.nodes_file.read_text())
if not isinstance(nodes,list) or not 1 <= len(nodes) <= 8 or len(set(nodes))!=len(nodes) or any(not isinstance(n,str) or not n or len(n)>253 or any(c not in "abcdefghijklmnopqrstuvwxyz0123456789.-" for c in n) for n in nodes):
 parser.error("nodes-file must contain 1-8 distinct Kubernetes node names")
image="ghcr.io/aviadhayumi/workload-map/karta-exporter@sha256:a33983d94d1363c1505bdd3b48f3221d86519f5ab5a50b7c0227a1d9a2459a2b"
control='monitoring'; target='karta-metrics-workloads';items=[]
def obj(kind,name,namespace=control,api='v1',**fields):
 return dict(apiVersion=api,kind=kind,metadata=dict(name=name,namespace=namespace,labels={'app.kubernetes.io/part-of':'karta-gpu-demo'}),**fields)
def account(name,ns=control): items.append(obj('ServiceAccount',name,ns))
def role(name,ns,sa,rules):
 items.append(obj('Role',name,ns,api='rbac.authorization.k8s.io/v1',rules=rules))
 items.append(obj('RoleBinding',name,ns,api='rbac.authorization.k8s.io/v1',subjects=[dict(kind='ServiceAccount',name=sa,namespace=control)],roleRef=dict(apiGroup='rbac.authorization.k8s.io',kind='Role',name=name)))
security=dict(runAsNonRoot=True,runAsUser=65532,runAsGroup=65532,seccompProfile=dict(type='RuntimeDefault'))
container_security=dict(allowPrivilegeEscalation=False,readOnlyRootFilesystem=True,capabilities=dict(drop=['ALL']))
def container(name,command,args,port=None):
 c=dict(name=name,image=image,command=[command],args=args,securityContext=container_security,resources=dict(requests=dict(cpu='10m',memory='32Mi'),limits=dict(cpu='300m',memory='128Mi')))
 if port:
  c['ports']=[dict(name='metrics-'+str(port),containerPort=port)]
  c['readinessProbe']=dict(httpGet=dict(path='/healthz',port=port),periodSeconds=5)
  c['livenessProbe']=dict(httpGet=dict(path='/healthz',port=port),periodSeconds=15)
 return c
def deploy(name,cs,sa=None):
 labels={'app.kubernetes.io/name':name,'app.kubernetes.io/part-of':'karta-gpu-demo'}
 spec=dict(securityContext=security,containers=cs,automountServiceAccountToken=bool(sa))
 if sa: spec['serviceAccountName']=sa
 items.append(obj('Deployment',name,api='apps/v1',spec=dict(replicas=1,strategy=dict(type='Recreate'),selector=dict(matchLabels={'app.kubernetes.io/name':name}),template=dict(metadata=dict(labels=labels),spec=spec))))
 ports=[dict(name=p['name'],port=p['containerPort'],targetPort=p['name']) for c in cs for p in c.get('ports',[])]
 svc=obj('Service',name,spec=dict(selector={'app.kubernetes.io/name':name},ports=ports));svc['metadata']['labels']['app.kubernetes.io/name']=name;items.append(svc)
 items.append(obj('ServiceMonitor',name,api='monitoring.coreos.com/v1',spec=dict(selector=dict(matchLabels={'app.kubernetes.io/name':name}),namespaceSelector=dict(matchNames=[control]),endpoints=[dict(port=p['name'],interval='10s',honorLabels=True,relabelings=[dict(action='replace',targetLabel='job',replacement=name)]) for p in ports])))
account('karta-demo-dcgm')
role('karta-demo-topology-reader','gpu-operator','karta-demo-dcgm',[dict(apiGroups=[''],resources=['configmaps'],resourceNames=['topology-'+n for n in nodes],verbs=['get'])])
deploy('karta-demo-dcgm',[container('source','/fake-gpu-source',['--namespace=gpu-operator','--nodes='+','.join(nodes),'--listen-address=:9400'],9400)],'karta-demo-dcgm')
account('karta-workload-gpu-observer')
role('karta-gpu-observer-reader',target,'karta-workload-gpu-observer',[dict(apiGroups=[''],resources=['pods'],verbs=['list']),dict(apiGroups=[''],resources=['configmaps'],resourceNames=['karta-gpu-demo-round'],verbs=['get'])])
deploy('karta-workload-gpu-observer',[container('observer','/workload-gpu-observer',['--namespace='+target,'--node=*','--source-url=http://karta-demo-dcgm.monitoring.svc:9400/metrics','--karta-url=http://karta-metrics-exporter.monitoring.svc:8080/metrics','--demo-configmap=karta-gpu-demo-round','--window=180s','--listen-address=:9092'],9092)],'karta-workload-gpu-observer')
deploy('karta-gpu-observer',[container('node-'+str(i),'/gpu-observer',['--node='+n,'--dcgm-url=http://karta-demo-dcgm.monitoring.svc:9400/nodes/'+n+'/metrics','--source=synthetic','--window=60s','--listen-address=:'+str(9093+i)],9093+i) for i,n in enumerate(nodes)])
account('gpu-demo-randomizer')
role('gpu-demo-randomizer',target,'gpu-demo-randomizer',[dict(apiGroups=[''],resources=['pods'],verbs=['list','patch']),dict(apiGroups=[''],resources=['configmaps'],resourceNames=['karta-gpu-demo-round'],verbs=['get','update'])])
# Round state is created separately once, so reapplying these resources preserves it.
c=container('randomizer','/gpu-demo-randomizer',['--namespace='+target,'--karta-url=http://karta-metrics-exporter.monitoring.svc:8080/metrics','--opt-in-label=app.kubernetes.io/part-of','--opt-in-value=karta-demo'])
c['env']=[dict(name='POD_UID',valueFrom=dict(fieldRef=dict(fieldPath='metadata.uid')))]
items.append(obj('CronJob','gpu-demo-randomizer',api='batch/v1',spec=dict(schedule='* * * * *',timeZone='Etc/UTC',concurrencyPolicy='Forbid',startingDeadlineSeconds=30,successfulJobsHistoryLimit=2,failedJobsHistoryLimit=2,jobTemplate=dict(spec=dict(backoffLimit=1,activeDeadlineSeconds=90,ttlSecondsAfterFinished=1200,template=dict(metadata=dict(labels={'app.kubernetes.io/name':'gpu-demo-randomizer'}),spec=dict(serviceAccountName='gpu-demo-randomizer',restartPolicy='Never',securityContext=security,containers=[c])))))))
args.output.write_text(json.dumps(dict(apiVersion='v1',kind='List',items=items),indent=2)+'\n')
print('Prepared',len(items),'resources; fixed read-only topology allowlist; Pod patches restricted to demo namespace.')
