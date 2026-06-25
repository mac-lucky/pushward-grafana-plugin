import React, { useCallback, useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { AppPluginMeta, GrafanaTheme2 } from '@grafana/data';
import { PluginPage } from '@grafana/runtime';
import { Alert, Button, Field, Icon, Input, LoadingPlaceholder, Stack, useStyles2 } from '@grafana/ui';
import { ConfigResponse, connectToAlerting, getConfig, sendTest, TestKind, webhookUrl } from '../api';
import { testIds } from '../components/testIds';

interface ConnectProps {
  meta: AppPluginMeta;
}

type Banner = { severity: 'success' | 'error' | 'info'; title: string; detail?: string };

function errorMessage(e: unknown): string {
  if (e instanceof Error) {
    return e.message;
  }
  // getBackendSrv rejects with a fetch-response-like object.
  if (e && typeof e === 'object') {
    const data = (e as { data?: { message?: string } }).data;
    if (data?.message) {
      return data.message;
    }
    const statusText = (e as { statusText?: string }).statusText;
    if (statusText) {
      return statusText;
    }
  }
  return String(e);
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
    <PluginPage>
      <div data-testid={testIds.connect.container} className={s.page}>
        <p className={s.lead}>
          Provision a Grafana service account and a <strong>PushWard</strong> webhook contact point in one click. Alerts
          routed to it loop back into this plugin, which builds the timeline and delivers it over APNs.
        </p>

        {banner && (
          <Alert severity={banner.severity} title={banner.title} onRemove={() => setBanner(undefined)}>
            {banner.detail}
          </Alert>
        )}

        {loading ? (
          <LoadingPlaceholder text="Loading status…" />
        ) : (
          <>
            <div className={s.statusRow}>
              <Icon name={connected ? 'check-circle' : 'exclamation-circle'} className={connected ? s.ok : s.warn} />
              <span>{connected ? 'Connected — the "PushWard" contact point is provisioned.' : 'Not connected yet.'}</span>
            </div>

            {!apiKeySet && (
              <Alert severity="warning" title="PushWard API key not set">
                Add your PushWard integration key on the Configuration page before sending tests — delivery to
                api.pushward.app will fail without it.
              </Alert>
            )}

            <Field label="Webhook URL" description="The contact point posts alerts here. Created automatically on connect.">
              {/* Show the absolute, sub-path-safe URL actually provisioned into the
                  contact point, not the backend's relative path, so it matches what
                  the alerting engine posts to on reverse-proxy/sub-path installs. */}
              <Input readOnly value={webhookUrl()} width={70} />
            </Field>

            <Stack direction="row" gap={1} wrap="wrap">
              <Button
                data-testid={testIds.connect.connectButton}
                icon="link"
                onClick={onConnect}
                disabled={connecting}
              >
                {connecting ? 'Connecting…' : connected ? 'Reconnect / rotate token' : 'Connect to Grafana Alerting'}
              </Button>
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
          </>
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
    font-size: ${theme.typography.h5.fontSize};
    margin-bottom: ${theme.spacing(2)};
  `,
  statusRow: css`
    display: flex;
    align-items: center;
    gap: ${theme.spacing(1)};
    margin-bottom: ${theme.spacing(2)};
  `,
  ok: css`
    color: ${theme.colors.success.text};
  `,
  warn: css`
    color: ${theme.colors.warning.text};
  `,
});
