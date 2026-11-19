// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

export { parseKubernetesJSON } from './json';
export { getKartaWasm } from './karta';
export type { Envelope, KartaWasm } from './karta';

export type { ReadOptions, ReferenceReader, ReferenceQuery, ReferenceValue } from './kartaUtil';
export { buildTree, evaluatePhases, listCatalog, rootInstances } from './kartaUtil';

export type {
  ComponentNode,
  ExtractedInstance,
  GroupVersionKind,
  InstanceNode,
  Karta,
  Scale,
  Workload,
  WorkloadStatus,
  WorkloadTree,
} from './karta.types';
