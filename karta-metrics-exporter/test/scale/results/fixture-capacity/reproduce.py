# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

import sys,time,threading,json,urllib.request,urllib.parse,pathlib
sys.path.insert(0,sys.argv[1])
import runtime_scale as r
import runtime_scale_test as tests
p=pathlib.Path('/tmp/fixture-capacity.prom')
if not p.exists():
 t=tests.OracleTests();t.setUp(); lines=t.exposition.splitlines(True)
 with p.open('w') as f:
  for i in range(100000):
   for line in lines[:-2]: f.write(line.replace('job-0',f'job-{i}').replace('pod-0-0',f'pod-{i}-0'))
  f.writelines(lines[-2:])
f=r.LoopbackAPI(100000,1)
for i in range(20000):f.change(i%750,change_pods=False)
e=f.start();stop=threading.Event();errors=[];pages=[0];oracles=[]
def watch(resource,path):
 try:
  with urllib.request.urlopen(e+path+'?watch=true&resourceVersion='+str(f.revision),timeout=40) as b:
   while not stop.is_set():
    if not b.readline():break
 except Exception as ex:errors.append(str(ex))
def lists():
 try:
  while not stop.is_set():
   token=''
   while not stop.is_set():
    q='?limit=500&continue='+urllib.parse.quote(token)
    req=urllib.request.Request(e+'/apis/batch/v1/namespaces/scale/jobs'+q,headers={'Accept':'application/json;as=PartialObjectMetadataList'})
    with urllib.request.urlopen(req,timeout=10) as b:body=json.load(b)
    pages[0]+=1;token=body['metadata']['continue']
    if not token:break
 except Exception as ex:errors.append(str(ex))
def oracle():
 while not stop.is_set():
  t=time.monotonic()
  with p.open('rb') as b:r.read_metrics(b,f)
  oracles.append(time.monotonic()-t)
for resource,path in [('jobs','/apis/batch/v1/jobs'),('pods','/api/v1/pods'),('namespaces','/api/v1/namespaces')]:threading.Thread(target=watch,args=(resource,path),daemon=True).start()
threading.Thread(target=lists,daemon=True).start();threading.Thread(target=oracle,daemon=True).start()
start=time.monotonic();cpu=time.process_time();samples=[]
for i in range(3000):
 due=start+i/100;time.sleep(max(0,due-time.monotonic()));before=time.monotonic();f.change(i%750,change_pods=False);after=time.monotonic();samples.append([before-due,after-before])
stop.set();result={'seconds':time.monotonic()-start,'cpu_seconds':time.process_time()-cpu,'events':len(samples),'max_lateness':max(x[0] for x in samples),'max_change_seconds':max(x[1] for x in samples),'samples':samples,'pages':pages[0],'oracle_seconds':oracles,'errors':errors,'api':f.snapshot()};pathlib.Path(sys.argv[2]).write_text(json.dumps(result));print({k:v for k,v in result.items() if k not in ('samples','api')})
