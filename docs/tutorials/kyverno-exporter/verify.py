#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Exercise the tutorial's real policies in its dedicated kind cluster."""

import datetime
import json
from pathlib import Path
import subprocess
import time
import urllib.parse

LAB = Path(__file__).resolve().parent
ROOT = LAB.parents[2]
ARTIFACTS = ROOT / '.artifacts' / 'kyverno-exporter-tutorial'
KUBECONFIG = ARTIFACTS / 'kubeconfig'
CONTEXT = 'kind-kyverno-exporter-tutorial'
START = time.monotonic()
RESULTS = []


def kubectl(*args, stdin=None):
    command = ['kubectl', '--kubeconfig', str(KUBECONFIG), '--context', CONTEXT, *args]
    result = subprocess.run(command, input=stdin, text=True, capture_output=True, timeout=45)
    if result.returncode:
        raise RuntimeError(result.stderr.strip())
    return result.stdout


def job(name):
    result = kubectl('-n', 'ai-lab', 'get', 'job', name, '--ignore-not-found', '-o', 'json')
    return json.loads(result) if result.strip() else None


def query(expression):
    path = '/api/v1/namespaces/monitoring/services/http:prometheus:9090/proxy/api/v1/query?'
    result = json.loads(kubectl('get', '--raw', path + urllib.parse.urlencode({'query': expression})))
    assert result['status'] == 'success', result
    return result['data']['result']


def record(name, detail):
    entry = {'check': name, 'elapsed_seconds': round(time.monotonic() - START), 'detail': detail}
    RESULTS.append(entry)
    print(json.dumps(entry), flush=True)
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    (ARTIFACTS / 'verification.json').write_text(json.dumps({
        'started_utc': STARTED_UTC, 'checks': RESULTS,
    }, indent=2) + '\n')


def until(description, predicate, timeout=240):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(5)
    raise AssertionError('Timed out: ' + description)


def suspended(name):
    value = job(name)
    return value is not None and value['spec'].get('suspend', False)


def pods_removed(workload_uid):
    pods = json.loads(kubectl('-n', 'ai-lab', 'get', 'pods', '-l',
                             'batch.kubernetes.io/controller-uid=' + workload_uid, '-o', 'json'))
    return not pods['items']


def untouched(names, duration):
    deadline = time.monotonic() + duration
    while time.monotonic() < deadline:
        for name in names:
            value = job(name)
            assert value is not None, name + ' was deleted too early'
            assert not value['spec'].get('suspend', False), name + ' was suspended too early'
        time.sleep(5)


def create_job(name, action, command):
    template = json.loads(kubectl('-n', 'ai-lab', 'get', 'job', 'unselected-worker', '-o', 'json'))
    spec = template['spec']
    spec.pop('selector', None)
    spec.pop('manualSelector', None)
    spec['suspend'] = False
    spec['template']['metadata'] = {}
    spec['template']['spec']['containers'][0]['command'] = ['sh', '-c', command]
    value = {'apiVersion': 'batch/v1', 'kind': 'Job', 'metadata': {
        'name': name, 'namespace': 'ai-lab', 'labels': {
            'tutorial.karta.run.ai/action': action, 'tutorial.karta.run.ai/test': 'true',
        },
    }, 'spec': spec}
    kubectl('create', '-f', '-', stdin=json.dumps(value))
    return job(name)['metadata']['uid']


def scale_exporter(replicas):
    kubectl('-n', 'karta-system', 'scale', 'deployment/dsx-workload-exporter', '--replicas=' + str(replicas))


STARTED_UTC = datetime.datetime.now(datetime.timezone.utc).isoformat()
if __name__ == '__main__':
    assert KUBECONFIG.is_file(), 'Create the tutorial cluster and its kubeconfig first'
    # Only this tutorial context and its explicitly labelled Jobs are changed.
    assert kubectl('config', 'current-context').strip() == CONTEXT
    for controller, verb in [('background', 'update'), ('cleanup', 'delete')]:
        for namespace, expected in [('ai-lab', 'yes'), ('default', 'no')]:
            permission = subprocess.run([
                'kubectl', '--kubeconfig', str(KUBECONFIG), '--context', CONTEXT,
                'auth', 'can-i', verb, 'jobs', '-n', namespace,
                '--as=system:serviceaccount:kyverno:kyverno-' + controller + '-controller',
            ], text=True, capture_output=True, timeout=30)
            assert permission.stdout.strip() == expected, permission.stdout + permission.stderr
    record('effective-rbac', 'Background update and cleanup delete allowed in ai-lab and denied in default')
    kubectl('delete', 'mutatingpolicy', 'tutorial-suspend-running', '--ignore-not-found')
    kubectl('delete', 'deletingpolicy', 'tutorial-delete-completed', '--ignore-not-found')
    kubectl('-n', 'ai-lab', 'delete', 'jobs', '-l', 'tutorial.karta.run.ai/test=true', '--wait=true')
    kubectl('create', '-f', str(LAB / 'manifests' / 'workloads.yaml'))
    initial_created = time.monotonic()
    first_report_uid = job('completed-report')['metadata']['uid']
    initial_worker_uid = job('running-worker')['metadata']['uid']
    kubectl('apply', '-f', str(LAB / 'manifests' / 'policies.yaml'))
    until('Running observation', lambda: bool(query('dsx_workload_status{namespace="ai-lab",workload_uid="' + initial_worker_uid + '",phase="Running",observation_valid="true"} == 1')))
    record('live-status', 'Current exporter reports a verified Running Job')
    untouched(['running-worker', 'completed-report'], 45)
    record('fresh-jobs-wait', 'Fresh Jobs survived 45 seconds after the first Running observation')
    def initial_actions_finished():
        worker_stopped = suspended('running-worker')
        report_deleted = job('completed-report') is None
        if worker_stopped or report_deleted:
            assert time.monotonic() - initial_created >= 120, 'Policy acted before the minimum window'
        return worker_stopped and report_deleted

    until('initial suspension and cleanup', initial_actions_finished, timeout=300)
    until('Suspended and deleted Job Pods removed', lambda: pods_removed(initial_worker_uid) and pods_removed(first_report_uid))
    record('positive-actions', 'Kyverno suspended running-worker and deleted completed-report; both Jobs\' Pods disappeared')
    for name in ['unselected-worker', 'unselected-report', 'failed-report']:
        assert job(name) is not None and not suspended(name), name
    record('negative-controls', 'Unselected Running/Completed Jobs and selected Failed Job remain untouched')
    second_report_uid = create_job('completed-report', 'delete', 'echo replacement-report')
    assert second_report_uid != first_report_uid
    eligible = query('tutorial:job_completed:eligible')
    assert not any(r['metric']['workload_uid'] == second_report_uid for r in eligible)
    until('replacement observed', lambda: bool(query('dsx_workload_status{namespace="ai-lab",workload_uid="' + second_report_uid + '",phase="Completed",observation_valid="true"} == 1')))
    untouched(['completed-report'], 65)
    assert not any(r['metric']['workload_uid'] == second_report_uid for r in query('tutorial:job_completed:eligible'))
    record('same-name-new-uid', 'Replacement was observed under its new UID and survived 65 seconds of healthy telemetry without inheriting eligibility')
    kubectl('-n', 'ai-lab', 'patch', 'job', 'running-worker', '--type=merge', '-p', json.dumps({
        'metadata': {'labels': {'tutorial.karta.run.ai/action': 'observe'}}, 'spec': {'suspend': False},
    }))
    until('manual resume', lambda: job('running-worker').get('status', {}).get('ready', 0) > 0)
    resumed_at = time.monotonic()
    control_uid = job('unselected-worker')['metadata']['uid']
    assert any(r['metric']['workload_uid'] == control_uid for r in query('tutorial:job_running:eligible'))
    try:
        scale_exporter(0)
        until('failed exporter scrape', lambda: bool(query('up{job="dsx-exporter"} == 0')), timeout=60)
        until('eligible recording removed', lambda: not query('tutorial:job_running:eligible'), timeout=30)
        kubectl('-n', 'ai-lab', 'label', 'job', 'unselected-worker',
                'tutorial.karta.run.ai/action=suspend', '--overwrite')
        outage_uid = create_job('outage-worker', 'suspend', 'sleep 86400')
        outage_report_uid = create_job('outage-report', 'delete', 'echo no-telemetry')
        untouched(['outage-worker', 'outage-report', 'completed-report', 'running-worker', 'unselected-worker'], 145)
        assert not query('tutorial:job_running:eligible')
        assert not query('tutorial:job_completed:eligible')
        record('exporter-outage', '145 seconds without exporter scrapes produced no eligible Jobs or policy actions')
        record('previously-eligible-outage', 'An already observed Running Job was opted in during the outage and remained active')
        kubectl('-n', 'monitoring', 'scale', 'deployment/prometheus', '--replicas=0')
        until('Prometheus Pod stopped', lambda: not json.loads(kubectl('-n', 'monitoring', 'get', 'pods', '-l', 'app=prometheus', '-o', 'json'))['items'], timeout=60)
        untouched(['outage-worker', 'outage-report', 'completed-report', 'running-worker', 'unselected-worker'], 65)
        record('prometheus-outage', '65 seconds with Prometheus stopped left selected Jobs unchanged')
    finally:
        try:
            kubectl('-n', 'ai-lab', 'label', 'job', 'unselected-worker',
                    'tutorial.karta.run.ai/action=observe', '--overwrite')
        finally:
            try:
                kubectl('-n', 'monitoring', 'scale', 'deployment/prometheus', '--replicas=1')
            finally:
                scale_exporter(1)
    until('Prometheus ready', lambda: json.loads(kubectl('-n', 'monitoring', 'get', 'deployment', 'prometheus', '-o', 'json')).get('status', {}).get('availableReplicas', 0) == 1, timeout=90)
    until('exporter recovered', lambda: bool(query('up{job="dsx-exporter"} == 1')), timeout=90)
    recovered_at = time.monotonic()
    untouched(['outage-worker', 'outage-report', 'completed-report', 'running-worker'], 60)
    assert not any(r['metric']['workload_uid'] == outage_uid for r in query('tutorial:job_running:eligible'))
    record('recovery-waits', 'A fresh minute of recovered telemetry was insufficient for action')
    until('post-recovery suspension', lambda: suspended('outage-worker'), timeout=240)
    # Polling can observe the first recovered scrape up to two intervals late.
    assert time.monotonic() - recovered_at >= 110, 'Action before the recovered window'
    record('suspension-recovery', 'Suspension resumed automatically after a new qualifying window')
    previous_execution = json.loads(kubectl('get', 'deletingpolicy', 'tutorial-delete-completed', '-o', 'json')).get('status', {}).get('lastExecutionTime')
    # Kyverno 1.19.1 can exhaust retries and drop this policy after HTTP errors.
    # Equivalent schedule changes requeue it without changing its frequency.
    for schedule in ['* * * * *', '*/1 * * * *']:
        kubectl('patch', 'deletingpolicy', 'tutorial-delete-completed', '--type=merge',
                '-p', json.dumps({'spec': {'schedule': schedule}}))
    until('cleanup schedule resumed', lambda: json.loads(kubectl('get', 'deletingpolicy', 'tutorial-delete-completed', '-o', 'json')).get('status', {}).get('lastExecutionTime') != previous_execution, timeout=120)
    until('completed Jobs removed after explicit requeue', lambda: job('outage-report') is None and job('completed-report') is None, timeout=120)
    until('Recovered suspension and cleanup removed Pods', lambda: all(pods_removed(uid) for uid in [outage_uid, outage_report_uid, second_report_uid]), timeout=120)
    record('cleanup-requeued', 'Explicit equivalent schedule changes resumed cleanup; eligible completed Jobs and their Pods were deleted')
    assert not suspended('running-worker') and job('running-worker').get('status', {}).get('ready', 0) > 0
    assert time.monotonic() - resumed_at > 120
    record('opt-out-resume', 'Opting out and resuming in one update kept running-worker active beyond the policy window')
    for name in ['unselected-worker', 'unselected-report', 'failed-report']:
        assert job(name) is not None and not suspended(name), name
    record('passed', 'All live checks passed; exporter and Prometheus restored to one replica')
