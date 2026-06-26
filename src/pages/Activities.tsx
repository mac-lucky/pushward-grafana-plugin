import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  InteractiveTable,
  LinkButton,
  LoadingPlaceholder,
  Text,
  useStyles2,
  type Column,
} from '@grafana/ui';
import { ActivitySummary, errorMessage, getActivities, getHistory, HistoryEntry } from '../api';
import { CONNECT_HREF } from '../constants';
import { RelativeTimeCell } from '../components/ui/RelativeTimeCell';
import { TemplateCell } from '../components/ui/TemplateCell';
import { testIds } from '../components/testIds';

function Activities() {
  const s = useStyles2(getStyles);
  const [activities, setActivities] = useState<ActivitySummary[]>([]);
  const [history, setHistory] = useState<HistoryEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | undefined>(undefined);

  // Used by the Refresh button (event-handler context - safe to flip `loading`).
  const load = useCallback(async () => {
    setLoading(true);
    setError(undefined);
    try {
      const [act, hist] = await Promise.all([getActivities(), getHistory()]);
      setActivities(act.activities ?? []);
      setHistory(hist.entries ?? []);
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
    Promise.all([getActivities(), getHistory()])
      .then(([act, hist]) => {
        if (!active) {
          return;
        }
        setActivities(act.activities ?? []);
        setHistory(hist.entries ?? []);
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

  const activityColumns = useMemo<Array<Column<ActivitySummary>>>(
    () => [
      {
        id: 'name',
        header: 'Name',
        cell: ({ row: { original } }) => <span>{original.name ?? original.slug ?? '—'}</span>,
      },
      { id: 'slug', header: 'Slug' },
      {
        id: 'state',
        header: 'State',
        cell: ({ row: { original } }) => (
          <Badge color={original.state === 'ended' ? 'darkgrey' : 'green'} text={original.state ?? 'unknown'} />
        ),
      },
      {
        id: 'template',
        header: 'Template',
        // The server nests the template under content, not at the top level.
        cell: ({ row: { original } }) => <TemplateCell template={original.content?.template} />,
      },
      {
        id: 'priority',
        header: 'Priority',
        cell: ({ row: { original } }) => <span>{original.priority ?? '—'}</span>,
      },
      {
        id: 'updated_at',
        header: 'Updated',
        cell: ({ row: { original } }) => <RelativeTimeCell value={original.updated_at} />,
      },
    ],
    []
  );

  const historyColumns = useMemo<Array<Column<HistoryEntry>>>(
    () => [
      {
        id: 'ts',
        header: 'Time',
        cell: ({ row: { original } }) => <RelativeTimeCell value={original.ts} />,
      },
      { id: 'alertname', header: 'Alert' },
      { id: 'action', header: 'Action' },
      { id: 'slug', header: 'Activity' },
      {
        id: 'ok',
        header: 'Result',
        cell: ({ row: { original } }) =>
          original.ok ? (
            <Badge color="green" icon="check" text="ok" />
          ) : (
            <Badge color="red" icon="exclamation-triangle" text="failed" />
          ),
      },
      { id: 'detail', header: 'Detail' },
    ],
    []
  );

  return (
    <PluginPage
      subTitle="Live Activities and recent deliveries, proxied live from api.pushward.app."
      actions={
        <Button variant="secondary" icon="sync" onClick={load} disabled={loading}>
          Refresh
        </Button>
      }
    >
      <div data-testid={testIds.activities.container}>
        {error && (
          <Alert severity="error" title="Could not load activities">
            {error}
          </Alert>
        )}

        {loading ? (
          <LoadingPlaceholder text="Loading activities…" />
        ) : (
          <>
            <section className={s.section}>
              <Text element="h2" variant="h4">
                Live Activities
              </Text>
              <p className={s.muted}>Currently running Live Activities streamed from your devices.</p>
              {activities.length === 0 ? (
                <EmptyState
                  variant="call-to-action"
                  message="No Live Activities are currently running."
                  button={
                    <LinkButton icon="link" href={CONNECT_HREF}>
                      Connect to Grafana Alerting
                    </LinkButton>
                  }
                />
              ) : (
                <InteractiveTable
                  columns={activityColumns}
                  data={activities}
                  getRowId={(row, index) => row.slug ?? `activity-${index}`}
                  pageSize={10}
                />
              )}
            </section>

            <section className={s.section}>
              <Text element="h2" variant="h4">
                Recent delivery log
              </Text>
              <p className={s.muted}>The most recent webhook deliveries handled by the embedded bridge.</p>
              {history.length === 0 ? (
                <EmptyState variant="not-found" message="No deliveries recorded yet." />
              ) : (
                <InteractiveTable
                  columns={historyColumns}
                  data={history}
                  getRowId={(row, index) => `${row.ts}-${row.slug}-${index}`}
                  pageSize={15}
                />
              )}
            </section>
          </>
        )}
      </div>
    </PluginPage>
  );
}

export default Activities;

const getStyles = (theme: GrafanaTheme2) => ({
  section: css`
    margin-top: ${theme.spacing(3)};
  `,
  muted: css`
    color: ${theme.colors.text.secondary};
    margin: ${theme.spacing(1, 0)};
  `,
});
