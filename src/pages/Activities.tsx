import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import {
  Alert,
  Badge,
  Button,
  ButtonGroup,
  ConfirmModal,
  Dropdown,
  EmptyState,
  InteractiveTable,
  LinkButton,
  LoadingPlaceholder,
  Menu,
  Stack,
  Text,
  Tooltip,
  useStyles2,
  type Column,
} from '@grafana/ui';
import {
  ActiveAlert,
  ActivitySummary,
  endActivity,
  errorMessage,
  getActive,
  getActivities,
  getHistory,
  HistoryEntry,
  silenceAlert,
  SilenceMatcher,
} from '../api';
import { CONNECT_HREF, PARAM_ALERTNAME, PARAM_RULE_UID } from '../constants';
import { RelativeTimeCell } from '../components/ui/RelativeTimeCell';
import { TemplateCell } from '../components/ui/TemplateCell';
import { testIds } from '../components/testIds';

type Notice = { severity: 'success' | 'error'; title: string; detail?: string };

const SILENCE_DURATIONS: Array<{ label: string; seconds: number }> = [
  { label: 'Silence 1 hour', seconds: 60 * 60 },
  { label: 'Silence 4 hours', seconds: 4 * 60 * 60 },
  { label: 'Silence 24 hours', seconds: 24 * 60 * 60 },
];

// matchersFor builds the alertmanager silence matchers for an activity: the rule
// UID label silences the whole rule (preferred), then the tracked alertname.
// fallbackAlertname (the activity's own name, which the bridge sets to the
// alertname) keeps silencing working after a config-save recreate empties the
// bridge's in-memory tracking. Returns undefined when nothing can be matched.
function matchersFor(active: ActiveAlert | undefined, fallbackAlertname?: string): SilenceMatcher[] | undefined {
  if (active?.ruleUid) {
    return [{ name: '__alert_rule_uid__', value: active.ruleUid, isRegex: false, isEqual: true }];
  }
  const alertname = active?.alertname || fallbackAlertname;
  if (alertname) {
    return [{ name: 'alertname', value: alertname, isRegex: false, isEqual: true }];
  }
  return undefined;
}

function Activities() {
  const s = useStyles2(getStyles);
  const [activities, setActivities] = useState<ActivitySummary[]>([]);
  const [history, setHistory] = useState<HistoryEntry[]>([]);
  const [active, setActive] = useState<ActiveAlert[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | undefined>(undefined);
  const [notice, setNotice] = useState<Notice | undefined>(undefined);
  const [busySlug, setBusySlug] = useState<string | undefined>(undefined);
  const [confirmEnd, setConfirmEnd] = useState<ActivitySummary | undefined>(undefined);

  // Used by the Refresh button (event-handler context - safe to flip `loading`).
  const load = useCallback(async () => {
    setLoading(true);
    setError(undefined);
    try {
      const [act, hist, act2] = await Promise.all([getActivities(), getHistory(), getActive()]);
      setActivities(act.activities ?? []);
      setHistory(hist.entries ?? []);
      setActive(act2.active ?? []);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, []);

  // Initial load: keep setState inside the promise callbacks (not the effect
  // body) so it doesn't trigger cascading renders.
  useEffect(() => {
    let alive = true;
    Promise.all([getActivities(), getHistory(), getActive()])
      .then(([act, hist, act2]) => {
        if (!alive) {
          return;
        }
        setActivities(act.activities ?? []);
        setHistory(hist.entries ?? []);
        setActive(act2.active ?? []);
        setError(undefined);
      })
      .catch((e) => {
        if (alive) {
          setError(errorMessage(e));
        }
      })
      .finally(() => {
        if (alive) {
          setLoading(false);
        }
      });
    return () => {
      alive = false;
    };
  }, []);

  const activeBySlug = useMemo(() => {
    const m = new Map<string, ActiveAlert>();
    for (const a of active) {
      if (a.slug) {
        m.set(a.slug, a);
      }
    }
    return m;
  }, [active]);

  // Deep-link filter from the alert-rule UI-extension "View in PushWard" link.
  const [filterCleared, setFilterCleared] = useState(false);
  const linkFilter = useMemo(() => {
    const q = new URLSearchParams(window.location.search);
    return { alertname: q.get(PARAM_ALERTNAME) ?? '', ruleUid: q.get(PARAM_RULE_UID) ?? '' };
  }, []);
  const filterActive = !filterCleared && Boolean(linkFilter.alertname || linkFilter.ruleUid);

  const shownActivities = useMemo(() => {
    if (!filterActive) {
      return activities;
    }
    return activities.filter((a) => {
      const act = activeBySlug.get(a.slug ?? '');
      if (linkFilter.ruleUid && act?.ruleUid === linkFilter.ruleUid) {
        return true;
      }
      if (linkFilter.alertname && (act?.alertname === linkFilter.alertname || a.name === linkFilter.alertname)) {
        return true;
      }
      return false;
    });
  }, [activities, activeBySlug, filterActive, linkFilter]);

  const onEnd = useCallback(
    async (slug: string) => {
      setBusySlug(slug);
      setNotice(undefined);
      try {
        await endActivity(slug);
        setNotice({ severity: 'success', title: 'Activity ended' });
        await load();
      } catch (e) {
        setNotice({ severity: 'error', title: 'Could not end activity', detail: errorMessage(e) });
      } finally {
        setBusySlug(undefined);
        setConfirmEnd(undefined);
      }
    },
    [load]
  );

  const onSilence = useCallback(
    async (slug: string, matchers: SilenceMatcher[], durationSec: number, label: string) => {
      setBusySlug(slug);
      setNotice(undefined);
      try {
        await silenceAlert({ matchers, durationSec, comment: `Silenced from PushWard (${label})` });
        setNotice({ severity: 'success', title: 'Alert silenced', detail: label });
      } catch (e) {
        setNotice({ severity: 'error', title: 'Could not silence alert', detail: errorMessage(e) });
      } finally {
        setBusySlug(undefined);
      }
    },
    []
  );

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
      {
        id: 'actions',
        header: 'Actions',
        cell: ({ row: { original } }) => {
          const slug = original.slug ?? '';
          const ended = original.state === 'ended';
          const busy = busySlug === slug;
          const matchers = matchersFor(activeBySlug.get(slug), original.name);
          if (ended) {
            return <span className={s.muted}>—</span>;
          }
          return (
            <Stack direction="row" gap={0.5} alignItems="center">
              <ButtonGroup>
                {matchers ? (
                  <Dropdown
                    overlay={
                      <Menu>
                        {SILENCE_DURATIONS.map((d) => (
                          <Menu.Item
                            key={d.seconds}
                            label={d.label}
                            onClick={() => onSilence(slug, matchers, d.seconds, d.label)}
                          />
                        ))}
                      </Menu>
                    }
                  >
                    <Button variant="secondary" size="sm" icon="bell-slash" disabled={busy || !slug}>
                      Silence
                    </Button>
                  </Dropdown>
                ) : (
                  <Tooltip content="No matching alert is currently firing for this activity, so it can't be silenced.">
                    <Button variant="secondary" size="sm" icon="bell-slash" disabled>
                      Silence
                    </Button>
                  </Tooltip>
                )}
              </ButtonGroup>
              <Button
                variant="destructive"
                size="sm"
                icon="square-shape"
                disabled={busy || !slug}
                onClick={() => setConfirmEnd(original)}
              >
                End
              </Button>
            </Stack>
          );
        },
      },
    ],
    [activeBySlug, busySlug, onSilence, s.muted]
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

        {notice && (
          <Alert severity={notice.severity} title={notice.title} onRemove={() => setNotice(undefined)}>
            {notice.detail}
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
              {filterActive && (
                <div className={s.filterRow}>
                  <Badge color="blue" icon="filter" text={`Filtered to ${linkFilter.alertname || linkFilter.ruleUid}`} />
                  <Button variant="secondary" size="sm" fill="text" icon="times" onClick={() => setFilterCleared(true)}>
                    Show all
                  </Button>
                </div>
              )}
              {shownActivities.length === 0 ? (
                <EmptyState
                  variant={filterActive ? 'not-found' : 'call-to-action'}
                  message={
                    filterActive
                      ? 'No running Live Activity matches this alert.'
                      : 'No Live Activities are currently running.'
                  }
                  button={
                    filterActive ? undefined : (
                      <LinkButton icon="link" href={CONNECT_HREF}>
                        Connect to Grafana Alerting
                      </LinkButton>
                    )
                  }
                />
              ) : (
                <InteractiveTable
                  columns={activityColumns}
                  data={shownActivities}
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

      {confirmEnd && (
        <ConfirmModal
          isOpen
          title="End Live Activity"
          body={`End "${confirmEnd.name ?? confirmEnd.slug}"? This removes it from the device. If the underlying alert is still firing it will not reappear unless it fires again.`}
          confirmText="End activity"
          onConfirm={() => onEnd(confirmEnd.slug ?? '')}
          onDismiss={() => setConfirmEnd(undefined)}
        />
      )}
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
  filterRow: css`
    display: flex;
    align-items: center;
    gap: ${theme.spacing(1)};
    margin-bottom: ${theme.spacing(1)};
  `,
});
