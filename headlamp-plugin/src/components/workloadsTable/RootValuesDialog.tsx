// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

import Alert from '@mui/material/Alert';
import Button from '@mui/material/Button';
import Dialog from '@mui/material/Dialog';
import DialogActions from '@mui/material/DialogActions';
import DialogContent from '@mui/material/DialogContent';
import DialogTitle from '@mui/material/DialogTitle';
import { useEffect, useState } from 'react';
import type { ExtractedInstance } from '../../lib/karta/karta.types';

export function RootValuesDialog({
  name,
  read,
  onClose,
}: {
  name: string;
  read: (signal: AbortSignal) => Promise<Record<string, ExtractedInstance>>;
  onClose: () => void;
}) {
  const [values, setValues] = useState<Record<string, ExtractedInstance>>();
  const [error, setError] = useState<string>();
  useEffect(() => {
    const controller = new AbortController();
    setValues(undefined);
    setError(undefined);
    read(controller.signal).then(
      result => {
        if (!controller.signal.aborted) setValues(result);
      },
      err => {
        if (!controller.signal.aborted)
          setError(err instanceof Error ? err.message : 'Unable to read root values');
      }
    );
    return () => controller.abort();
  }, [read]);

  return (
    <Dialog open onClose={onClose} fullWidth>
      <DialogTitle>Root component values for {name}</DialogTitle>
      <DialogContent>
        {error ? (
          <Alert severity="warning">{error}</Alert>
        ) : values ? (
          Object.keys(values).length ? (
            Object.entries(values).map(([id, value]) => (
              <section key={id}>
                {id && <h3>{id}</h3>}
                <dl>
                  {value.fragmentedPodSpec?.image && (
                    <>
                      <dt>Image</dt>
                      <dd>{value.fragmentedPodSpec.image}</dd>
                    </>
                  )}
                  {value.scale?.replicas !== undefined && (
                    <>
                      <dt>Replicas</dt>
                      <dd>{value.scale.replicas}</dd>
                    </>
                  )}
                </dl>
                <details
                  open={!value.fragmentedPodSpec?.image && value.scale?.replicas === undefined}
                >
                  <summary>Mapped fields</summary>
                  <pre>
                    {JSON.stringify(
                      value,
                      (_key, field) => (typeof field === 'bigint' ? String(field) : field),
                      2
                    )}
                  </pre>
                </details>
              </section>
            ))
          ) : (
            'No root instances'
          )
        ) : (
          'Loading values...'
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
}
