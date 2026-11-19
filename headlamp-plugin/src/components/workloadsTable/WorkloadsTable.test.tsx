// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { dataTableProps, clustersRef } = vi.hoisted(() => ({
  dataTableProps: [] as any[],
  clustersRef: { current: ['cluster-a'] as string[] },
}));

vi.mock('@kinvolk/headlamp-plugin/lib', () => ({
  K8s: { useSelectedClusters: () => clustersRef.current },
}));
vi.mock('../dataTable/DataTable', () => ({
  DataTable: (props: any) => {
    dataTableProps.push(props);
    const cell = props.columns.find((c: any) => c.id === 'workload').Cell;
    return (
      <>
        {props.data?.map((row: any) => (
          <span key={row.id}>{cell({ row: { original: row } })}</span>
        ))}
      </>
    );
  },
}));

import { WorkloadsTable } from './WorkloadsTable';

// Node 24 exposes its own localStorage, which shadows jsdom's and throws
// unless the process was started with a store file. The real CommonComponents
// pulls in Headlamp's redux store, which reads localStorage at import time
// (plugin/pluginConfigSlice.js), so the stub has to exist before that import.
vi.hoisted(() => {
  const storage = new Map<string, string>();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => void storage.set(key, String(value)),
      removeItem: (key: string) => void storage.delete(key),
      clear: () => storage.clear(),
    },
  });
});

beforeEach(() => {
  dataTableProps.length = 0;
  clustersRef.current = ['cluster-a'];
});

describe('WorkloadsTable', () => {
  it('reads effective root values only when the workload opens and cancels on close', async () => {
    const readRoot = vi
      .fn()
      .mockResolvedValue({
        '': { fragmentedPodSpec: { image: 'trainer:v2' }, scale: { replicas: 2 } },
      });
    render(
      <WorkloadsTable
        rows={[
          {
            id: 'job',
            name: 'training',
            namespace: 'demo',
            cluster: 'cluster-a',
            kind: 'TrainJob',
            apiGroup: 'trainer.kubeflow.org',
            creationTimestamp: '',
            phases: ['Running'],
            componentsCount: 1,
            readRoot,
          },
        ]}
      />
    );
    expect(readRoot).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'training' }));
    expect(await screen.findByText('trainer:v2')).toBeTruthy();
    expect(screen.getByText('Replicas')).toBeTruthy();
    expect(screen.getByText('2')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(readRoot.mock.calls[0][0].aborted).toBe(true);
  });

  // The age accessor is negated, so ascending is newest first. Sorting it
  // descending would open the table on the oldest workload.
  it('opens sorted newest first', () => {
    render(<WorkloadsTable rows={[]} loading={false} />);

    expect(dataTableProps.slice(-1)[0]?.initialSortColumnId).toBe('age');
    expect(dataTableProps.slice(-1)[0]?.initialSortDesc).toBe(false);
  });

  it('shows the cluster column only when more than one cluster is selected', () => {
    render(<WorkloadsTable rows={[]} loading={false} />);
    expect(dataTableProps.slice(-1)[0]?.columns.some((c: any) => c.id === 'cluster')).toBe(false);

    clustersRef.current = ['cluster-a', 'cluster-b'];
    render(<WorkloadsTable rows={[]} loading={false} />);
    expect(dataTableProps.slice(-1)[0]?.columns.some((c: any) => c.id === 'cluster')).toBe(true);
  });
});
