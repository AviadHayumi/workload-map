// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

import { beforeEach, describe, expect, it, vi } from 'vitest';
const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock('@kinvolk/headlamp-plugin/lib', () => ({ ApiProxy: { request } }));
import { clusterReader, rereadWorkload } from './clusterReader';

const signal = new AbortController().signal;
const query = {
  binding: 'runtime',
  apiVersion: 'trainer.kubeflow.org/v1alpha1',
  kind: 'ClusterTrainingRuntime',
  namespace: 'job-ns',
  name: 'torch',
};
const groupPath = '/apis/trainer.kubeflow.org/v1alpha1';
const discovery = {
  resources: [
    { name: 'clustertrainingruntimes/status', kind: query.kind, namespaced: false },
    { name: 'clustertrainingruntimes', kind: query.kind, namespaced: false },
  ],
};
describe('Headlamp Kubernetes reference reader', () => {
  beforeEach(() => request.mockReset());
  it('uses cluster scope from discovery and maps only object 404 to a missing lookup', async () => {
    const read = clusterReader('selected', signal);
    const missing = Object.assign(new Error('gone'), { status: 404 });
    request.mockResolvedValueOnce(discovery).mockRejectedValueOnce(missing);
    await expect(read(query)).resolves.toEqual({ object: null });
    expect(request.mock.calls).toEqual([
      [groupPath, { cluster: 'selected', signal }, false, true],
      [
        groupPath + '/clustertrainingruntimes/torch',
        { cluster: 'selected', signal, isJSON: false },
        false,
        true,
      ],
    ]);
    const denied = Object.assign(new Error('denied'), { status: 403 });
    request.mockRejectedValueOnce(denied);
    await expect(read(query)).rejects.toBe(denied);
    request.mockRejectedValueOnce(missing);
    await expect(clusterReader('selected', signal)(query)).rejects.toBe(missing);
  });

  it('keeps scope and selector on every page and preserves empty lists', async () => {
    const read = clusterReader('selected', signal);
    const pods = {
      binding: 'pods',
      apiVersion: 'v1',
      kind: 'Pod',
      namespace: 'demo',
      selector: 'role in (trainer,worker)',
    };
    const item = { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'pod', namespace: 'demo' } };
    request
      .mockResolvedValueOnce({ resources: [{ name: 'pods', kind: 'Pod', namespaced: true }] })
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ items: [], metadata: { continue: 'page/2' } }))
      )
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [item] })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [] })));
    await expect(read(pods)).resolves.toEqual({ list: [item] });
    for (const [rawPath, options] of request.mock.calls.slice(1)) {
      const url = new URL(rawPath, 'https://example.test');
      expect(url.pathname).toBe('/api/v1/namespaces/demo/pods');
      expect(url.searchParams.get('labelSelector')).toBe(pods.selector);
      expect(options).toEqual({ cluster: 'selected', signal, isJSON: false });
    }
    expect(
      new URL(request.mock.calls[2][0], 'https://example.test').searchParams.get('continue')
    ).toBe('page/2');
    await expect(read(pods)).resolves.toEqual({ list: [] });
    await expect(read({ ...pods, namespace: '' })).rejects.toThrow(
      'invalid Kubernetes reference path'
    );
  });

  it('aborts before dispatch and rejects invalid discovery paths', async () => {
    const aborted = new AbortController();
    aborted.abort();
    await expect(clusterReader('selected', aborted.signal)(query)).rejects.toThrow();
    await expect(
      clusterReader('selected', signal)({ ...query, apiVersion: '../v1' })
    ).rejects.toThrow();
    expect(request).not.toHaveBeenCalled();
  });
});

it('preserves a referenced int64 and only uses an exact reread of the observed workload', async () => {
  const workload = {
    apiVersion: 'v1',
    kind: 'Pod',
    metadata: { name: 'p', namespace: 'ns', uid: 'original', resourceVersion: '1' },
  };
  const body = JSON.stringify(workload).slice(0, -1) + ',"sequence":9007199254740993}';
  request.mockReset().mockResolvedValueOnce(new Response(body));
  const fresh = await rereadWorkload('selected', workload, 'pods', true, signal);
  expect(fresh.sequence).toBe(BigInt('9007199254740993'));
  request.mockResolvedValueOnce(new Response(body));
  await expect(
    rereadWorkload(
      'selected',
      { ...workload, metadata: { ...workload.metadata, uid: 'other' } },
      'pods',
      true,
      signal
    )
  ).rejects.toThrow('changed');

  request.mockResolvedValueOnce(discovery).mockResolvedValueOnce(new Response(body));
  expect(await clusterReader('selected', signal)(query)).toEqual({ object: fresh });
});
