import React, { useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import { Alert, Card, Divider, Grid, Icon, LoadingPlaceholder, Text, useStyles2 } from '@grafana/ui';
import { ApiKeyStatus, ConfigResponse, errorMessage, getConfig, getHealthz, HealthzResponse } from '../api';
import { ACTIVITIES_HREF, CONFIG_HREF, CONNECT_HREF } from '../constants';
import { testIds } from '../components/testIds';
import { PageHero } from '../components/ui/PageHero';
import { StatusCard, StatusCardProps } from '../components/ui/StatusCard';

type LoadState =
  | { status: 'loading' }
  | { status: 'error'; error: string }
  | { status: 'ready'; health: HealthzResponse; config: ConfigResponse };

const ok = (title: string, description: string): StatusCardProps => ({
  tone: 'success',
  icon: 'check-circle',
  title,
  description,
});

const warn = (title: string, description: string, action?: StatusCardProps['action']): StatusCardProps => ({
  tone: 'warning',
  icon: 'exclamation-triangle',
  title,
  description,
  action,
});

// The API-key card is tri-state: distinguish a rejected key (error) from a
// transient 'unknown' probe blip (warning), and from no key at all.
function apiKeyCard(status: ApiKeyStatus, keySet: boolean): StatusCardProps {
  if (!keySet) {
    return warn('API key missing', 'Add your PushWard integration key to deliver.', { label: 'Add key', href: CONFIG_HREF });
  }
  switch (status) {
    case 'valid':
      return ok('API key valid', 'Authorized for api.pushward.app.');
    case 'rejected':
      return {
        tone: 'error',
        icon: 'times-circle',
        title: 'API key rejected',
        description: 'The key was refused by api.pushward.app.',
        action: { label: 'Update key', href: CONFIG_HREF },
      };
    default:
      return {
        tone: 'warning',
        icon: 'question-circle',
        title: 'API key unverified',
        description: 'Could not verify the key right now.',
      };
  }
}

function statusCards(health: HealthzResponse, config: ConfigResponse): StatusCardProps[] {
  return [
    health.ok
      ? ok('Backend healthy', 'The plugin bridge is responding.')
      : { tone: 'error', icon: 'exclamation-triangle', title: 'Backend degraded', description: 'The plugin backend is not healthy.' },
    apiKeyCard(health.apiKeyStatus, config.apiKeySet),
    health.datasource
      ? ok('Datasource configured', 'History queries are enabled.')
      : warn('No datasource', 'Pick a Prometheus datasource to build timelines.', { label: 'Choose datasource', href: CONFIG_HREF }),
    health.history
      ? ok('Timeline history ready', 'Sparkline history can be queried.')
      : warn('Timeline history unavailable', 'Needs a datasource and a Grafana token.'),
    config.webhookConnected
      ? ok('Alerting connected', 'The PushWard contact point is provisioned.')
      : warn('Not connected', 'Run the Connect wizard to wire up alerting.', { label: 'Open Connect', href: CONNECT_HREF }),
    health.widgets
      ? ok('Widgets publishing', 'Configured widgets are streaming to PushWard.')
      : { tone: 'neutral', icon: 'minus-circle', title: 'Widgets idle', description: 'No widgets configured (optional).' },
  ];
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
          setState({ status: 'error', error: errorMessage(e) });
        }
      });
    return () => {
      active = false;
    };
  }, []);

  return (
    <PluginPage>
      <div data-testid={testIds.overview.container} className={s.page}>
        <PageHero
          title="PushWard"
          tagline={
            <>
              Grafana alerts <Icon name="arrow-right" /> rich iOS Live Activities, delivered over APNs.
            </>
          }
        />

        <section className={s.section}>
          <Text element="h2" variant="h4">
            How it works
          </Text>
          <p className={s.muted}>
            This plugin is the in-Grafana setup and management layer. Alerts still flow out of Grafana through a regular{' '}
            <strong>webhook contact point</strong> that the Connect wizard creates for you &mdash; the webhook loops back
            into this plugin&apos;s backend, which resolves the rule&apos;s PromQL, queries history through your Grafana
            datasource, builds the timeline, and pushes it to PushWard.
          </p>
          <Alert severity="info" title="It is not a native contact-point type">
            Grafana hardcodes contact-point types in core, so no plugin can add &quot;PushWard&quot; to the contact-point
            dropdown. Delivery flows through the auto-created webhook contact point instead. Running on Grafana Cloud
            additionally requires a signed build.
          </Alert>
        </section>

        <section className={s.section}>
          <Text element="h2" variant="h4">
            Status
          </Text>
          <div className={s.sectionBody}>
            {state.status === 'loading' && <LoadingPlaceholder text="Checking status…" />}
            {state.status === 'error' && (
              <Alert severity="error" title="Could not reach the plugin backend">
                {state.error}
              </Alert>
            )}
            {state.status === 'ready' && (
              <>
                <div data-testid={testIds.overview.status}>
                  <Grid minColumnWidth={34} gap={2}>
                    {statusCards(state.health, state.config).map((card) => (
                      <StatusCard key={card.title} {...card} />
                    ))}
                  </Grid>
                </div>
                {state.health.widgetsError && (
                  <Alert severity="warning" title="Widget configuration error">
                    {state.health.widgetsError}
                  </Alert>
                )}
                {state.health.message && <p className={s.muted}>{state.health.message}</p>}
              </>
            )}
          </div>
        </section>

        <section className={s.section}>
          <Text element="h2" variant="h4">
            Get started
          </Text>
          <div className={s.sectionBody}>
            <Grid minColumnWidth={44} gap={2}>
              <Card href={CONFIG_HREF}>
                <Card.Figure>
                  <Icon name="cog" size="xxl" />
                </Card.Figure>
                <Card.Heading>Configure</Card.Heading>
                <Card.Description>Set your PushWard API key and the history datasource.</Card.Description>
              </Card>
              <Card href={CONNECT_HREF}>
                <Card.Figure>
                  <Icon name="link" size="xxl" />
                </Card.Figure>
                <Card.Heading>Connect to Alerting</Card.Heading>
                <Card.Description>Provision the webhook contact point in one click.</Card.Description>
              </Card>
              <Card href={ACTIVITIES_HREF}>
                <Card.Figure>
                  <Icon name="list-ul" size="xxl" />
                </Card.Figure>
                <Card.Heading>View Live Activities</Card.Heading>
                <Card.Description>See running activities and the recent delivery log.</Card.Description>
              </Card>
            </Grid>
          </div>
        </section>

        <Divider spacing={3} />
        <p className={s.footer}>
          <Icon name="external-link-alt" /> Learn more at{' '}
          <a className={s.link} href="https://pushward.app" target="_blank" rel="noreferrer">
            pushward.app
          </a>
        </p>
      </div>
    </PluginPage>
  );
}

export default Overview;

const getStyles = (theme: GrafanaTheme2) => ({
  page: css`
    max-width: 980px;
  `,
  section: css`
    margin-top: ${theme.spacing(3)};
  `,
  sectionBody: css`
    margin-top: ${theme.spacing(1.5)};
  `,
  muted: css`
    color: ${theme.colors.text.secondary};
    margin-top: ${theme.spacing(1)};
  `,
  footer: css`
    color: ${theme.colors.text.secondary};
  `,
  link: css`
    color: ${theme.colors.text.link};
    text-decoration: underline;
  `,
});
