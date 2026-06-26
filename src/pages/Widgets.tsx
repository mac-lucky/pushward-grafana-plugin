import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import {
  Alert,
  Button,
  EmptyState,
  InteractiveTable,
  LinkButton,
  LoadingPlaceholder,
  Text,
  useStyles2,
  type Column,
} from '@grafana/ui';
import { errorMessage, getWidgets, WidgetSummary } from '../api';
import { CONFIG_HREF } from '../constants';
import { RelativeTimeCell } from '../components/ui/RelativeTimeCell';
import { TemplateCell } from '../components/ui/TemplateCell';
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
      setError(errorMessage(e));
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
          setError(errorMessage(e));
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
        cell: ({ row: { original } }) => <TemplateCell template={original.content?.template} />,
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
        cell: ({ row: { original } }) => <RelativeTimeCell value={original.updated_at} />,
      },
    ],
    []
  );

  return (
    <PluginPage
      subTitle="Widgets published to PushWard as standalone Live Activities, proxied live from api.pushward.app."
      actions={
        <Button variant="secondary" icon="sync" onClick={load} disabled={loading}>
          Refresh
        </Button>
      }
    >
      <div data-testid={testIds.widgets.container}>
        {error && (
          <Alert severity="error" title="Could not load widgets">
            {error}
          </Alert>
        )}

        {loading ? (
          <LoadingPlaceholder text="Loading widgets..." />
        ) : (
          <section className={s.section}>
            <Text element="h2" variant="h4">
              Published widgets
            </Text>
            <p className={s.muted}>Each widget is a scheduled PromQL query rendered as its own Live Activity.</p>
            {widgets.length === 0 ? (
              <EmptyState
                variant="call-to-action"
                message="No widgets are published yet."
                button={
                  <LinkButton icon="cog" href={CONFIG_HREF}>
                    Configure widgets
                  </LinkButton>
                }
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
  muted: css`
    color: ${theme.colors.text.secondary};
    margin: ${theme.spacing(1, 0)};
  `,
});
