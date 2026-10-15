# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
import os
import json,os,time,torch
assert torch.cuda.is_available(), "CUDA unavailable"
torch.set_num_threads(1)
device=torch.device("cuda:0")
a=torch.randn((4096,4096),device=device,dtype=torch.float16)
b=torch.randn_like(a);out=torch.empty_like(a)
reserve=torch.ones((512*1024*1024,),device=device,dtype=torch.float32)
torch.cuda.synchronize()
role=os.environ["TEST_ROLE"]
print(json.dumps({"event":"ready","role":role,"gpu":torch.cuda.get_device_name(0),"torch":torch.__version__,"cuda":torch.version.cuda,"allocated_bytes":torch.cuda.memory_allocated()}),flush=True)
phases=[("active" if role=="master" else "idle",120),("idle",240),("active" if role=="master" else "idle",90),("idle",30)]
for phase,duration in phases:
 print(json.dumps({"event":"phase","role":role,"phase":phase,"seconds":duration,"time":time.time()}),flush=True)
 deadline=time.monotonic()+duration;iterations=0
 while time.monotonic()<deadline:
  if phase=="active":
   torch.mm(a,b,out=out);torch.cuda.synchronize();iterations+=1
  else:time.sleep(1)
 print(json.dumps({"event":"phase_complete","phase":phase,"iterations":iterations}),flush=True)
assert torch.isfinite(out).all().item() if role=="master" else True
print(json.dumps({"event":"completed","role":role}),flush=True)
