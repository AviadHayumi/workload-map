# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import concurrent.futures,json,pathlib,re,urllib.request,urllib.parse,os
b=pathlib.Path(__file__).resolve().parent
base='http://127.0.0.1:19090/karta/prometheus/api/v1/query?'
def query(q):
 return json.load(urllib.request.urlopen(base+urllib.parse.urlencode(dict(query=q, **({'time':os.environ['QUERY_TIME']} if 'QUERY_TIME' in os.environ else {}))),timeout=30))
uid=query('karta_workload_status{workload="real-dra-mig-cycle"}')['data']['result'][0]['metric']['workload_uid']
variables={'namespace':'karta-metrics-workloads','workload':'real-dra-mig-cycle','workload_uid':uid,'component':'.*','component_instance':'.*','gpu_device':'.*','metric':'DCGM_FI_PROF_GR_ENGINE_ACTIVE','__rate_interval':'2m','__interval':'30s','__range':'15m','__range_s':'900'}
work=[]
def visit(panels,deck):
 for p in panels:
  for t in p.get('targets',[]):
   if t.get('expr'):
    q=t['expr']
    q=re.sub(r'\$\{([a-zA-Z_]+)(?::[^}]+)?\}',lambda m:variables.get(m[1],m[0]),q)
    q=re.sub(r'\$([a-zA-Z_]+)',lambda m:variables.get(m[1],m[0]),q)
    work.append((deck,p['id'],p['title'],q))
  visit(p.get('panels',[]),deck)
for cm in json.loads((b.parent/'dashboard.json').read_text())['items']:
 for key,raw in cm.get('data',{}).items():visit(json.loads(raw)['panels'],key)
def check(item):
 deck,panel,title,q=item
 try:
  r=query(q);return {'dashboard':deck,'panel':panel,'title':title,'query':q,'success':r['status']=='success','series':len(r.get('data',{}).get('result',[])),'error':r.get('error')}
 except Exception as e:return {'dashboard':deck,'panel':panel,'title':title,'query':q,'success':False,'error':str(e)}
with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:results=list(pool.map(check,work))
(b/'query-validation.json').write_text(json.dumps(results,indent=2));failed=[r for r in results if not r['success']];print(json.dumps({'queries':len(results),'failed':failed,'nonempty':sum(r.get('series',0)>0 for r in results)},indent=2));assert not failed
