// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

import { parseKubernetesJSON, stringifyKubernetesJSON } from './json';
import { Envelope, getKartaWasm } from './karta';
import { ExtractedInstance, Karta, Workload, WorkloadTree } from './karta.types';

function unwrap<T>(envelope: Envelope, fallback: T): T {
  if (envelope.error !== null) {
    throw new Error(envelope.error);
  }
  if (envelope.data === null) {
    return fallback;
  }
  return parseKubernetesJSON<T>(envelope.data);
}

export interface ReferenceQuery {
  binding: string;
  apiVersion: string;
  kind: string;
  namespace: string;
  name?: string;
  selector?: string;
}

export type ReferenceValue = { object: Workload | null } | { list: Workload[] };

export type ReferenceReader = (query: ReferenceQuery) => Promise<ReferenceValue>;

export type ReadOptions = { referencesEnabled?: boolean } & (
  | { references?: Record<string, ReferenceValue>; reader?: never }
  | { references?: never; reader: ReferenceReader }
);

async function read<T>(
  operation: 'buildTree' | 'rootInstances' | 'evaluatePhases',
  definition: Karta,
  workload: Workload,
  options: ReadOptions,
  fallback: T
): Promise<T> {
  const karta = await getKartaWasm();
  const definitionJSON = JSON.stringify(definition);
  const workloadJSON = stringifyKubernetesJSON(workload);
  const { referencesEnabled, references, reader } = options;
  if (references !== undefined && reader) {
    throw new Error('supply either reference objects or a reader');
  }
  const first = karta[operation](
    definitionJSON,
    workloadJSON,
    stringifyKubernetesJSON({
      referencesEnabled,
      references: referencesEnabled === false ? undefined : references,
    })
  );
  if (!first.needsReferences || !reader || referencesEnabled === false) {
    return unwrap(first, fallback);
  }
  const queries = unwrap<ReferenceQuery[]>(
    karta.referenceQueries(definitionJSON, workloadJSON),
    []
  );
  const entries = [];
  for (const query of queries) {
    try {
      entries.push([query.binding, await reader(query)]);
    } catch (error) {
      if ((error as Error)?.name === 'AbortError') throw error;
      const status = (error as { status?: number })?.status;
      const operation = query.name === undefined ? 'list' : 'lookup';
      throw new Error(
        `reference "${query.binding}" ${operation} failed${status ? ` (HTTP ${status})` : ''}`
      );
    }
  }
  return unwrap(
    karta[operation](
      definitionJSON,
      workloadJSON,
      stringifyKubernetesJSON({ references: Object.fromEntries(entries) })
    ),
    fallback
  );
}

export function buildTree(
  definition: Karta,
  workload: Workload,
  options: ReadOptions = {}
): Promise<WorkloadTree> {
  return read('buildTree', definition, workload, options, { Status: null, Children: [] });
}

export function rootInstances(
  definition: Karta,
  workload: Workload,
  options: ReadOptions = {}
): Promise<Record<string, ExtractedInstance>> {
  return read('rootInstances', definition, workload, options, {});
}

export function evaluatePhases(
  definition: Karta,
  workload: Workload,
  options: ReadOptions = {}
): Promise<string[]> {
  return read('evaluatePhases', definition, workload, options, []);
}

export async function listCatalog(): Promise<Karta[]> {
  const karta = await getKartaWasm();
  return unwrap(karta.listCatalog(), []);
}
