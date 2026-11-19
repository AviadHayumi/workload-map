// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

export class UnsafeIntegerError extends Error {
  constructor() {
    super('use the original Kubernetes JSON for integers outside the JavaScript safe range');
  }
}

// JavaScript rounds large integers before calling the reviver. Modern runtimes
// expose the original token as context.source; keep that integer as a bigint.
export function parseKubernetesJSON<T>(raw: string): T {
  return JSON.parse(raw, (_key, value, context?: { source?: string }) => {
    if (typeof value === 'number' && Number.isInteger(value) && !Number.isSafeInteger(value)) {
      if (context?.source && /^-?\d+$/.test(context.source)) return BigInt(context.source);
      if (!context?.source) throw new UnsafeIntegerError();
    }
    return value;
  }) as T;
}

export function stringifyKubernetesJSON(value: unknown): string {
  return JSON.stringify(value, (_key, field) => {
    if (typeof field === 'bigint') {
      const raw = (JSON as typeof JSON & { rawJSON?: (s: string) => unknown }).rawJSON;
      if (!raw) throw new UnsafeIntegerError();
      return raw(String(field));
    }
    if (typeof field === 'number' && Number.isInteger(field) && !Number.isSafeInteger(field)) {
      throw new UnsafeIntegerError();
    }
    return field;
  });
}
