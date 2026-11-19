// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

import { ApiProxy } from '@kinvolk/headlamp-plugin/lib';
import { parseKubernetesJSON, UnsafeIntegerError } from './json';
import type { Workload } from './karta.types';
import type { ReferenceReader } from './kartaUtil';

interface APIResource {
  name: string;
  kind: string;
  namespaced: boolean;
}

function segment(value: string): string {
  // Kubernetes forbids these path segment names, including URL-encoded slashes.
  if (!value || value === '.' || value === '..' || /[/ %]/.test(value)) {
    throw new Error('invalid Kubernetes reference path');
  }
  return encodeURIComponent(value);
}

async function readJSON<T>(cluster: string, path: string, signal: AbortSignal): Promise<T> {
  const response: Response = await ApiProxy.request(
    path,
    { cluster, signal, isJSON: false },
    false,
    true
  );
  return parseKubernetesJSON<T>(await response.text());
}

// A workload delivered by Headlamp's JSON watch can already contain rounded
// integers. An exact reread is usable only for the same object and revision.
export async function rereadWorkload(
  cluster: string,
  workload: Workload,
  plural: string,
  namespaced: boolean,
  signal: AbortSignal
): Promise<Workload> {
  const parts = workload.apiVersion.split('/');
  if (!cluster || parts.length > 2) throw new Error('invalid workload cluster or API version');
  const prefix = parts.length === 1 ? '/api/' : '/apis/';
  const scope = namespaced ? `/namespaces/${segment(workload.metadata.namespace ?? '')}` : '';
  const path =
    prefix +
    parts.map(segment).join('/') +
    scope +
    '/' +
    segment(plural) +
    '/' +
    segment(workload.metadata.name);
  const fresh = await readJSON<Workload>(cluster, path, signal);
  if (
    !workload.metadata.uid ||
    !workload.metadata.resourceVersion ||
    fresh.metadata.uid !== workload.metadata.uid ||
    fresh.metadata.resourceVersion !== workload.metadata.resourceVersion
  ) {
    throw new Error('workload changed during evaluation');
  }
  return fresh;
}

export async function readObserved<T>(
  workload: Workload,
  cluster: string,
  plural: string,
  namespaced: boolean,
  signal: AbortSignal,
  evaluate: (exact: Workload) => Promise<T>
): Promise<T> {
  try {
    return await evaluate(workload);
  } catch (error) {
    if (!(error instanceof UnsafeIntegerError)) throw error;
    const exact = await rereadWorkload(cluster, workload, plural, namespaced, signal);
    return evaluate(exact);
  }
}

// One reader per observation. Scope and resource names come from this cluster's discovery.
export function clusterReader(cluster: string, signal: AbortSignal): ReferenceReader {
  const discovery = new Map<string, Promise<{ resources: APIResource[] }>>();
  return async query => {
    signal.throwIfAborted();
    if (!cluster) throw new Error('reference requires a cluster');
    const parts = query.apiVersion.split('/');
    if (parts.length > 2) {
      throw new Error('invalid Kubernetes reference API version');
    }
    const groupPath = (parts.length === 1 ? '/api/' : '/apis/') + parts.map(segment).join('/');
    let info = discovery.get(groupPath);
    if (!info) {
      info = ApiProxy.request(groupPath, { cluster, signal }, false, true);
      discovery.set(groupPath, info);
    }
    const resource = (await info).resources.find(
      r => r.kind === query.kind && !r.name.includes('/')
    );
    if (!resource || typeof resource.namespaced !== 'boolean') {
      throw new Error(`discovery has no resource for ${query.apiVersion} ${query.kind}`);
    }
    const scope = resource.namespaced ? `/namespaces/${segment(query.namespace)}` : '';
    const path = `${groupPath}${scope}/${segment(resource.name)}`;
    if (query.name !== undefined) {
      try {
        const object = await readJSON<Workload>(cluster, `${path}/${segment(query.name)}`, signal);
        return { object };
      } catch (error) {
        if ((error as { status?: number })?.status === 404) {
          return { object: null };
        }
        throw error;
      }
    }

    const list: Workload[] = [];
    if (!query.selector) throw new Error('reference list requires a selector');
    const params = new URLSearchParams({ labelSelector: query.selector, limit: '500' });
    const tokens = new Set<string>();
    do {
      signal.throwIfAborted();
      const page = await readJSON<{ items: Workload[]; metadata?: { continue?: string } }>(
        cluster,
        `${path}?${params}`,
        signal
      );
      list.push(...page.items);
      if (!page.metadata?.continue) {
        return { list };
      }
      if (tokens.has(page.metadata.continue))
        throw new Error('repeated reference continuation token');
      tokens.add(page.metadata.continue);
      params.set('continue', page.metadata.continue);
    } while (true);
  };
}
