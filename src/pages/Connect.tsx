import React, { useCallback, useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { AppPluginMeta, GrafanaTheme2 } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import { Alert, Button, ClipboardButton, Field, Input, LoadingPlaceholder, Stack, Text, TextLink, useStyles2 } from '@grafana/ui';
import { ConfigResponse, connectToAlerting, errorMessage, getConfig, sendTest, TestKind, webhookUrl } from '../api';
import { CONFIG_HREF } from '../constants';
import { testIds } from '../components/testIds';
import { StatusCard } from '../components/ui/StatusCard';
import { BRAND_ACCENT } from '../components/ui/brand';

interface ConnectProps {
  meta: AppPluginMeta;
}

type Banner = { severity: 'success' | 'error' | 'info'; title: string; detail?: string };

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  const s = useStyles2(getStyles);
  return (
    <div className={s.step}>
      <div className={s.stepHeader}>
        <span className={s.stepNum}>{n}</span>
        <Text element="h2" variant="h5">
          {title}
        </Text>
      </div>
      <div className={s.stepBody}>{children}</div>
    </div>
  );
}

function Connect({ meta }: ConnectProps) {
  const s = useStyles2(getStyles);
  const [config, setConfig] = useState<ConfigResponse | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [connecting, setConnecting] = useState(false);
  const [testing, setTesting] = useState<TestKind | undefined>(undefined);
  const [banner, setBanner] = useState<Banner | undefined>(undefined);

  // Re-fetch status after the Connect action. setState lives in event-handler
  // context here, so it's safe to flip `loading` synchronously.
  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setConfig(await getConfig());
    } catch (e) {
      setBanner({ severity: 'error', title: 'Could not load plugin status', detail: errorMessage(e) });
    } finally {
      setLoading(false);
    }
  }, []);

  // Initial load: keep setState inside the promise callbacks (not the effect
  // body) so it doesn't trigger cascading renders.
  useEffect(() => {
    let active = true;
    getConfig()
      .then((cfg) => {
        if (active) {
          setConfig(cfg);
        }
      })
      .catch((e) => {
        if (active) {
          setBanner({ severity: 'error', title: 'Could not load plugin status', detail: errorMessage(e) });
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

  const onConnect = async () => {
    setConnecting(true);
    setBanner(undefined);
    try {
      await connectToAlerting(meta.enabled ?? true, meta.pinned ?? false);
      setBanner({
        severity: 'success',
        title: 'Connected to Grafana Alerting',
        detail: 'Created the "PushWard" webhook contact point. Add it to a notification policy or alert rule to start delivering.',
      });
      await refresh();
    } catch (e) {
      setBanner({ severity: 'error', title: 'Failed to connect', detail: errorMessage(e) });
    } finally {
      setConnecting(false);
    }
  };

  const onTest = async (kind: TestKind) => {
    setTesting(kind);
    setBanner(undefined);
    try {
      const res = await sendTest(kind);
      setBanner({
        severity: res.ok ? 'success' : 'error',
        title: res.ok ? 'Test sent' : 'Test failed',
        detail: res.message,
      });
    } catch (e) {
      setBanner({ severity: 'error', title: 'Test failed', detail: errorMessage(e) });
    } finally {
      setTesting(undefined);
    }
  };

  const connected = Boolean(config?.webhookConnected);
  const apiKeySet = Boolean(config?.apiKeySet);

  return (
    <PluginPage subTitle="Provision a Grafana service account and a PushWard webhook contact point in one click.">
      <div data-testid={testIds.connect.container} className={s.page}>
        <p className={s.lead}>
          Alerts routed to the contact point loop back into this plugin, which builds the timeline and delivers it over
          APNs.
        </p>

        {banner && (
          <Alert severity={banner.severity} title={banner.title} onRemove={() => setBanner(undefined)}>
            {banner.detail}
          </Alert>
        )}

        {loading ? (
          <LoadingPlaceholder text="Loading status…" />
        ) : (
          <Stack direction="column" gap={3}>
            <StatusCard
              tone={connected ? 'success' : 'warning'}
              icon={connected ? 'check-circle' : 'exclamation-circle'}
              title={connected ? 'Connected' : 'Not connected yet'}
              description={
                connected
                  ? 'The "PushWard" contact point is provisioned and ready.'
                  : 'Connect to provision the webhook contact point.'
              }
            />

            {!apiKeySet && (
              <Alert severity="warning" title="PushWard API key not set">
                Add your PushWard integration key on the{' '}
                <TextLink href={CONFIG_HREF} inline>
                  Configuration page
                </TextLink>{' '}
                before sending tests &mdash; delivery to api.pushward.app will fail without it.
              </Alert>
            )}

            <Step n={1} title="Connect to Grafana Alerting">
              <p className={s.muted}>
                Creates a Viewer service account and a webhook contact point named &quot;PushWard&quot;. Safe to re-run to
                rotate the token.
              </p>
              <Field label="Webhook URL" description="The contact point posts alerts here. Created automatically on connect.">
                {/* Show the absolute, sub-path-safe URL actually provisioned into the
                    contact point, not the backend's relative path, so it matches what
                    the alerting engine posts to on reverse-proxy/sub-path installs. */}
                <Stack direction="row" gap={1} alignItems="center">
                  <Input readOnly value={webhookUrl()} width={70} />
                  <ClipboardButton variant="secondary" icon="copy" getText={webhookUrl}>
                    Copy
                  </ClipboardButton>
                </Stack>
              </Field>
              <div className={s.actions}>
                <Button data-testid={testIds.connect.connectButton} icon="link" onClick={onConnect} disabled={connecting}>
                  {connecting ? 'Connecting…' : connected ? 'Reconnect / rotate token' : 'Connect to Grafana Alerting'}
                </Button>
              </div>
            </Step>

            <Step n={2} title="Test delivery">
              <p className={s.muted}>
                Send a one-off notification or fire a sample timeline to confirm the round trip to your device.
              </p>
              <Stack direction="row" gap={1} wrap="wrap">
                <Button
                  data-testid={testIds.connect.testNotification}
                  variant="secondary"
                  icon="bell"
                  onClick={() => onTest('notification')}
                  disabled={testing !== undefined || !apiKeySet}
                >
                  {testing === 'notification' ? 'Sending…' : 'Send test notification'}
                </Button>
                <Button
                  data-testid={testIds.connect.testTimeline}
                  variant="secondary"
                  icon="graph-bar"
                  onClick={() => onTest('timeline')}
                  disabled={testing !== undefined || !apiKeySet}
                >
                  {testing === 'timeline' ? 'Sending…' : 'Fire test timeline'}
                </Button>
              </Stack>
            </Step>
          </Stack>
        )}
      </div>
    </PluginPage>
  );
}

export default Connect;

const getStyles = (theme: GrafanaTheme2) => ({
  page: css`
    max-width: 820px;
  `,
  lead: css`
    color: ${theme.colors.text.secondary};
    margin-bottom: ${theme.spacing(2)};
  `,
  muted: css`
    color: ${theme.colors.text.secondary};
    margin-bottom: ${theme.spacing(1.5)};
  `,
  step: css`
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
    padding: ${theme.spacing(2.5)};
  `,
  stepHeader: css`
    display: flex;
    align-items: center;
    gap: ${theme.spacing(1.5)};
    margin-bottom: ${theme.spacing(2)};
  `,
  stepNum: css`
    flex: 0 0 auto;
    width: 28px;
    height: 28px;
    border-radius: 50%;
    background: ${BRAND_ACCENT};
    color: #ffffff;
    font-weight: ${theme.typography.fontWeightBold};
    display: inline-flex;
    align-items: center;
    justify-content: center;
  `,
  stepBody: css`
    padding-left: ${theme.spacing(5)};
  `,
  actions: css`
    margin-top: ${theme.spacing(1)};
  `,
});
