# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Read the designated fixtures and produce independent, label-based expectations."""
import datetime
import json
from fixture_common import NS, oracle_path, run, validate_environment

def require(condition, detail):
    if not condition:
        raise SystemExit(f"Fixture oracle mismatch: {detail}")


validate_environment()
output = oracle_path()
resources = ('pods,deployments.apps,replicasets.apps,statefulsets.apps,jobs.batch,'
             'jobsets.jobset.x-k8s.io,leaderworkersets.leaderworkerset.x-k8s.io,'
             'bundles.discovery-test.example.com')
data = json.loads(run('get', resources, '-n', NS, '-o', 'json'))
objects = data['items']
by_key = {(o['kind'], o['metadata']['name']): o for o in objects}
pods = [o for o in objects if o['kind'] == 'Pod']
require(len(objects) == 32, len(objects))
require(len(pods) == 14, len(pods))

def identity(o):
    return {k: o['metadata'][k] for k in ('namespace', 'name', 'uid')} | {'apiVersion': o['apiVersion'], 'kind': o['kind']}

def selected(key, value):
    return [p for p in pods if p['metadata'].get('labels', {}).get(key) == value]

def instance(key, members, api_object=None):
    result = {'key': key, 'podUIDs': sorted(p['metadata']['uid'] for p in members), 'podNames': sorted(p['metadata']['name'] for p in members)}
    if api_object is not None:
        result['apiObject'] = identity(api_object)
    return result

def group(key, members, instances):
    return {'key': key, 'podUIDs': sorted(p['metadata']['uid'] for p in members), 'instances': instances}

workloads = []
def add(kind, name, members, groups):
    workloads.append({'root': identity(by_key[kind, name]), 'podUIDs': sorted(p['metadata']['uid'] for p in members), 'groups': groups})

js = selected('jobset.sigs.k8s.io/jobset-name', 'sdk-jobset')
js_groups = []
for role, count in [('chief', 1), ('workers', 2)]:
    members = [p for p in js if p['metadata']['labels']['jobset.sigs.k8s.io/replicatedjob-name'] == role]
    require(len(members) == count, 'len(members) == count')
    instances = []
    for index in range(count):
        matching = [p for p in members if p['metadata']['labels']['jobset.sigs.k8s.io/job-index'] == str(index)]
        require(len(matching) == 1, 'len(matching) == 1')
        job = by_key['Job', f'sdk-jobset-{role}-{index}']
        require(matching[0]['metadata']['ownerReferences'][0]['uid'] == job['metadata']['uid'], "matching[0]['metadata']['ownerReferences'][0]['uid'] == job['metadata']['uid']")
        require(job['metadata']['ownerReferences'][0]['uid'] == by_key['JobSet', 'sdk-jobset']['metadata']['uid'], "job['metadata']['ownerReferences'][0]['uid'] == by_key['JobSet', 'sdk-jobset']['metadata']['uid']")
        instances.append(instance(str(index), matching, job))
    js_groups.append(group(role, members, instances))
add('JobSet', 'sdk-jobset', js, js_groups)

lws = selected('leaderworkerset.sigs.k8s.io/name', 'sdk-lws')
lws_groups = []
for index in range(2):
    members = [p for p in lws if p['metadata']['labels']['leaderworkerset.sigs.k8s.io/group-index'] == str(index)]
    require({p['metadata']['name'] for p in members} == {f'sdk-lws-{index}', f'sdk-lws-{index}-1'}, "{p['metadata']['name'] for p in members} == {f'sdk-lws-{index}', f'sdk-lws-{index}-1'}")
    instances = []
    for worker in range(2):
        matching = [p for p in members if p['metadata']['labels']['leaderworkerset.sigs.k8s.io/worker-index'] == str(worker)]
        require(len(matching) == 1, 'len(matching) == 1')
        instances.append(instance('leader' if worker == 0 else 'worker-1', matching, matching[0]))
    lws_groups.append(group(str(index), members, instances))
add('LeaderWorkerSet', 'sdk-lws', lws, lws_groups)

members = selected('app', 'sdk-deployment')
require(len(members) == 2, 'len(members) == 2')
add('Deployment', 'sdk-deployment', members, [group('default', members, [instance(p['metadata']['name'], [p], p) for p in members])])

bundle_pods = []
bundle_groups = []
for name, count in [('sdk-generic-a', 2), ('sdk-generic-b', 1)]:
    deployment = by_key['Deployment', name]
    require(deployment['metadata']['ownerReferences'][0]['uid'] == by_key['Bundle', 'sdk-bundle']['metadata']['uid'], "deployment['metadata']['ownerReferences'][0]['uid'] == by_key['Bundle', 'sdk-bundle']['metadata']['uid']")
    members = selected('app', name)
    require(len(members) == count, 'len(members) == count')
    bundle_pods.extend(members)
    bundle_groups.append(group(name, members, [instance(p['metadata']['name'], [p], p) for p in members]))
add('Bundle', 'sdk-bundle', bundle_pods, bundle_groups)

standalone = by_key['Pod', 'sdk-standalone']
require(not standalone['metadata'].get('ownerReferences'), "not standalone['metadata'].get('ownerReferences')")
add('Pod', 'sdk-standalone', [standalone], [group('default', [standalone], [instance('sdk-standalone', [standalone], standalone)])])
require(by_key['Deployment', 'sdk-zero']['spec']['replicas'] == 0, "by_key['Deployment', 'sdk-zero']['spec']['replicas'] == 0")
require(not selected('app', 'sdk-zero'), "not selected('app', 'sdk-zero')")
add('Deployment', 'sdk-zero', [], [group('default', [], [])])
finite = selected('batch.kubernetes.io/job-name', 'sdk-finite')
require(len(finite) == 1 and finite[0]['status']['phase'] == 'Succeeded', "len(finite) == 1 and finite[0]['status']['phase'] == 'Succeeded'")
add('Job', 'sdk-finite', finite, [group('default', finite, [instance(p['metadata']['name'], [p], p) for p in finite])])

# Translate the independent label partitions to the intended Karta terminology.
for workload in workloads:
    partitions = workload.pop('groups')
    if workload['root']['kind'] == 'JobSet':
        workload['semanticComponents'] = [{'name': 'replicatedjob', 'instances': [
            {'name': partition['key'], 'podUIDs': partition['podUIDs'], 'replicas': [
                {'key': replica['key'], 'podUIDs': replica['podUIDs'], 'apiObject': replica['apiObject']}
                for replica in partition['instances']]}
            for partition in partitions]}]
    elif workload['root']['kind'] == 'LeaderWorkerSet':
        workload['semanticComponents'] = [{'name': 'group', 'instances': [{'name': '', 'replicas': [
            {'key': partition['key'], 'podUIDs': partition['podUIDs'], 'components': [
                {'name': 'leader' if member['key'] == 'leader' else 'worker', 'instances': [
                    {'name': '', 'podUIDs': member['podUIDs']}]} for member in partition['instances']]}
            for partition in partitions]}]}]
    else:
        workload['fixturePartitions'] = [
            {'name': partition['key'], 'podUIDs': partition['podUIDs']}
            for partition in partitions]
all_uids = [uid for w in workloads for uid in w['podUIDs']]
require(len(all_uids) == len(set(all_uids)) == len(pods), 'len(all_uids) == len(set(all_uids)) == len(pods)')
require(set(all_uids) == {p['metadata']['uid'] for p in pods}, "set(all_uids) == {p['metadata']['uid'] for p in pods}")
result = {'license': 'SPDX-License-Identifier: Apache-2.0; Copyright (c) 2026 NVIDIA Corporation', 'capturedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'namespace': NS, 'source': 'Direct Kubernetes API; fixture names and upstream operator labels; no Karta SDK calls', 'objectCount': len(objects), 'podCount': len(pods), 'workloads': workloads, 'apiObjects': [identity(o) | {'owners': o['metadata'].get('ownerReferences', [])} for o in objects]}
output.parent.mkdir(parents=True, exist_ok=True)
output.write_text(json.dumps(result, indent=2) + '\n')
print(f'Oracle: {len(workloads)} roots, {len(pods)} unique Pods, {len(objects)} API objects')
