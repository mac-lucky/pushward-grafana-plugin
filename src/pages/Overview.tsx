import React, { useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import { Alert, Badge, Icon, LinkButton, LoadingPlaceholder, Stack, useStyles2 } from '@grafana/ui';
import { ConfigResponse, getConfig, getHealthz, HealthzResponse } from '../api';
import { PLUGIN_BASE_URL, PLUGIN_ID, ROUTES } from '../constants';
import { testIds } from '../components/testIds';

type LoadState =
  | { status: 'loading' }
  | { status: 'error'; error: string }
  | { status: 'ready'; health: HealthzResponse; config: ConfigResponse };

function statusBadge(ok: boolean, okText: string, badText: string) {
  return <Badge color={ok ? 'green' : 'orange'} icon={ok ? 'check' : 'exclamation-triangle'} text={ok ? okText : badText} />;
}

function Overview() {
  const s = useStyles2(getStyles);
  const [state, setState] = useState<LoadState>({ status: 'loading' });

  useEffect(() => {
    let active = true;
    Promise.all([getHealthz(), getConfig()])
      .then(([health, config]) => {
        if (active) {
          setState({ status: 'ready', health, config });
        }
      })
      .catch((e) => {
        if (active) {
          setState({ status: 'error', error: e instanceof Error ? e.message : String(e) });
        }
      });
    return () => {
      active = false;
    };
  }, []);

  return (
    <PluginPage>
      <div data-testid={testIds.overview.container} className={s.page}>
        <p className={s.lead}>
          PushWard turns Grafana alerts into rich <strong>iOS Live Activities</strong> — a live, history-backed timeline on
          the Lock Screen and Dynamic Island, delivered over APNs.
        </p>

        <section className={s.section}>
          <h3 className={s.h3}>How it works</h3>
          <p className={s.muted}>
            This plugin is the in-Grafana setup and management layer. Alerts still flow out of Grafana through a regular{' '}
            <strong>webhook contact point</strong> that the Connect wizard creates for you — the webhook loops back into
            this plugin&apos;s backend, which resolves the rule&apos;s PromQL, queries history through your Grafana
            datasource, builds the timeline, and pushes it to PushWard.
          </p>
          <Alert severity="info" title="It is not a native contact-point type">
            Grafana hardcodes contact-point types in core, so no plugin can add &quot;PushWard&quot; to the contact-point
            dropdown. Delivery flows through the auto-created webhook contact point instead. Running on Grafana Cloud
            additionally requires a signed build.
          </Alert>
        </section>

        <section className={s.section}>
          <h3 className={s.h3}>Status</h3>
          {state.status === 'loading' && <LoadingPlaceholder text="Checking status…" />}
          {state.status === 'error' && (
            <Alert severity="error" title="Could not reach the plugin backend">
              {state.error}
            </Alert>
          )}
          {state.status === 'ready' && (
            <Stack direction="column" gap={1}>
              <div data-testid={testIds.overview.status}>
                <Stack direction="row" gap={1} wrap="wrap">
                  {statusBadge(state.health.ok, 'Backend healthy', 'Backend degraded')}
                  {statusBadge(state.config.apiKeySet, 'PushWard API key set', 'API key missing')}
                  {statusBadge(state.health.datasource, 'Datasource configured', 'No datasource')}
                  {statusBadge(state.config.webhookConnected, 'Alerting connected', 'Not connected')}
                </Stack>
              </div>
              {state.health.message && <p className={s.muted}>{state.health.message}</p>}
            </Stack>
          )}
        </section>

        <section className={s.section}>
          <h3 className={s.h3}>Get started</h3>
          <Stack direction="row" gap={1} wrap="wrap">
            <LinkButton icon="cog" variant="secondary" href={`/plugins/${PLUGIN_ID}`}>
              Configure API key &amp; datasource
            </LinkButton>
            <LinkButton icon="link" href={`${PLUGIN_BASE_URL}/${ROUTES.Connect}`}>
              Connect to Grafana Alerting
            </LinkButton>
            <LinkButton icon="list-ul" variant="secondary" href={`${PLUGIN_BASE_URL}/${ROUTES.Activities}`}>
              View Live Activities
            </LinkButton>
          </Stack>
        </section>

        <p className={s.footer}>
          <Icon name="external-link-alt" /> Learn more at{' '}
          <a className={s.link} href="https://pushward.app" target="_blank" rel="noreferrer">
            pushward.app
          </a>
          .
        </p>
      </div>
    </PluginPage>
  );
}

export default Overview;

const getStyles = (theme: GrafanaTheme2) => ({
  page: css`
    max-width: 820px;
  `,
  lead: css`
    font-size: ${theme.typography.h5.fontSize};
    margin-bottom: ${theme.spacing(2)};
  `,
  section: css`
    margin-top: ${theme.spacing(3)};
  `,
  h3: css`
    margin-bottom: ${theme.spacing(1)};
  `,
  muted: css`
    color: ${theme.colors.text.secondary};
  `,
  footer: css`
    margin-top: ${theme.spacing(4)};
    color: ${theme.colors.text.secondary};
  `,
  link: css`
    color: ${theme.colors.text.link};
    text-decoration: underline;
  `,
});
