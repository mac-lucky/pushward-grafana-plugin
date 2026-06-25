import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import {
  Alert,
  Button,
  EmptyState,
  InteractiveTable,
  LoadingPlaceholder,
  Stack,
  useStyles2,
  type Column,
} from '@grafana/ui';
import { getWidgets, WidgetSummary } from '../api';
import { formatRfc3339 } from '../dates';
import { testIds } from '../components/testIds';

function formatValue(value: unknown): string {
  if (value === undefined || value === null || value === '') {
    return '-';
  }
  if (typeof value === 'object') {
    return JSON.stringify(value);
  }
  return String(value);
}

function Widgets() {
  const s = useStyles2(getStyles);
  const [widgets, setWidgets] = useState<WidgetSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | undefined>(undefined);

  // Used by the Refresh button (event-handler context - safe to flip `loading`).
  const load = useCallback(async () => {
    setLoading(true);
    setError(undefined);
    try {
      const res = await getWidgets();
      setWidgets(res.widgets ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  // Initial load: keep setState inside the promise callbacks (not the effect
  // body) so it doesn't trigger cascading renders.
  useEffect(() => {
    let active = true;
    getWidgets()
      .then((res) => {
        if (!active) {
          return;
        }
        setWidgets(res.widgets ?? []);
        setError(undefined);
      })
      .catch((e) => {
        if (active) {
          setError(e instanceof Error ? e.message : String(e));
        }
      })
      .finally(() => {
        if (active) {
          setLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, []);

  const columns = useMemo<Array<Column<WidgetSummary>>>(
    () => [
      {
        id: 'name',
        header: 'Name',
        cell: ({ row: { original } }) => <span>{original.name ?? original.slug ?? '-'}</span>,
      },
      { id: 'slug', header: 'Slug' },
      {
        id: 'template',
        header: 'Template',
        cell: ({ row: { original } }) => <span>{original.content?.template ?? '-'}</span>,
      },
      {
        id: 'value',
        header: 'Value',
        cell: ({ row: { original } }) => <span>{formatValue(original.content?.value)}</span>,
      },
      {
        id: 'unit',
        header: 'Unit',
        cell: ({ row: { original } }) => <span>{original.content?.unit ?? '-'}</span>,
      },
      {
        id: 'updated_at',
        header: 'Updated',
        cell: ({ row: { original } }) => <span>{formatRfc3339(original.updated_at)}</span>,
      },
    ],
    []
  );

  return (
    <PluginPage>
      <div data-testid={testIds.widgets.container}>
        <Stack direction="row" justifyContent="flex-end">
          <Button variant="secondary" icon="sync" onClick={load} disabled={loading}>
            Refresh
          </Button>
        </Stack>

        {error && (
          <Alert severity="error" title="Could not load widgets">
            {error}
          </Alert>
        )}

        {loading ? (
          <LoadingPlaceholder text="Loading widgets..." />
        ) : (
          <section className={s.section}>
            <h3 className={s.h3}>Published widgets</h3>
            <p className={s.muted}>
              Widgets configured here are published to PushWard as standalone Live Activities, proxied live from
              api.pushward.app.
            </p>
            {widgets.length === 0 ? (
              <EmptyState
                variant="not-found"
                message="No widgets are published. Configure them on the plugin's Configuration page."
              />
            ) : (
              <InteractiveTable
                columns={columns}
                data={widgets}
                getRowId={(row, index) => row.slug ?? `widget-${index}`}
                pageSize={15}
              />
            )}
          </section>
        )}
      </div>
    </PluginPage>
  );
}

export default Widgets;

const getStyles = (theme: GrafanaTheme2) => ({
  section: css`
    margin-top: ${theme.spacing(3)};
  `,
  h3: css`
    margin-bottom: ${theme.spacing(1)};
  `,
  muted: css`
    color: ${theme.colors.text.secondary};
    margin-bottom: ${theme.spacing(1)};
  `,
});
