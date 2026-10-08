import React, { ChangeEvent, SubmitEvent, useEffect, useState } from 'react';
import { lastValueFrom } from 'rxjs';
import { css } from '@emotion/css';
import {
  AppPluginMeta,
  DataSourceInstanceSettings,
  GrafanaTheme2,
  PluginConfigPageProps,
  PluginMeta,
} from '@grafana/data';
import { DataSourcePicker, getBackendSrv } from '@grafana/runtime';
import {
  Button,
  Combobox,
  ControlledCollapse,
  Field,
  FieldSet,
  Input,
  RadioButtonGroup,
  SecretInput,
  Switch,
  Text,
  TextArea,
  useStyles2,
  type ComboboxOption,
} from '@grafana/ui';
import { errorMessage, getConfig } from '../../api';
import { CONNECT_HREF } from '../../constants';
import { testIds } from '../testIds';
import { WidgetBuilder } from './WidgetBuilder';

// One widget definition. Mirrors the standalone bridge's widget config; kept loose
// here because the backend owns validation - the form only round-trips the JSON.
export type WidgetConfig = {
  slug: string;
  name?: string;
  template: string;
  [key: string]: unknown;
};

export type AppPluginSettings = {
  apiUrl?: string;
  datasourceUid?: string;
  severityLabel?: string;
  defaultSeverity?: string;
  priority?: number;
  historyWindow?: string;
  pollInterval?: string;
  cleanupDelay?: string;
  staleTimeout?: string;
  smoothing?: boolean;
  scale?: string;
  decimals?: number;
  alsoNotify?: boolean;
  notifyLevel?: string;
  ackEnabled?: boolean;
  ackRepeatSeconds?: number;
  ackExpireSeconds?: number;
  widgets?: WidgetConfig[];
};

const DEFAULTS: Required<AppPluginSettings> = {
  apiUrl: 'https://api.pushward.app',
  datasourceUid: '',
  severityLabel: 'severity',
  defaultSeverity: 'warning',
  priority: 5,
  historyWindow: '30m',
  pollInterval: '30s',
  cleanupDelay: '15m',
  staleTimeout: '24h',
  smoothing: true,
  scale: 'linear',
  decimals: 1,
  alsoNotify: false,
  notifyLevel: 'active',
  ackEnabled: false,
  ackRepeatSeconds: 300,
  ackExpireSeconds: 3600,
  widgets: [],
};

const SCALE_OPTIONS: Array<ComboboxOption<string>> = [
  { label: 'Linear', value: 'linear' },
  { label: 'Logarithmic', value: 'logarithmic' },
];

// Push-notification interruption level, mapped to the PushWard server's own
// level enum: Silent -> passive, Normal -> active, Critical -> critical.
const NOTIFY_LEVEL_OPTIONS: Array<{ label: string; value: string }> = [
  { label: 'Silent', value: 'passive' },
  { label: 'Normal', value: 'active' },
  { label: 'Critical', value: 'critical' },
];

// checkE2EKey mirrors the backend's key parse closely enough to stop a save
// that would leave every alert push content-free. Returns the key with
// whitespace removed, or an error message.
const checkE2EKey = (typed: string): { key: string; error?: string } => {
  const key = typed.replace(/\s+/g, '');
  if (/^hl[ka]_/i.test(key)) {
    return {
      key,
      error: 'That is an integration key. Paste the encryption key from Settings > End-to-End Encryption in the PushWard app.',
    };
  }
  if (!/^[0-9a-f]{64}$/i.test(key)) {
    return { key, error: 'An encryption key is 64 hexadecimal characters.' };
  }
  return { key };
};

// Consistent field widths so the form columns line up: wider for free text,
// narrower for numbers and durations.
const TEXT_WIDTH = 40;
const NUM_WIDTH = 24;

// Two-widget starter: a stat_list and a gauge. The PromQL is placeholder - the
// user edits it for their own metrics. Icons are SF Symbol names (rendered by
// the iOS app), not Grafana UI icon ids.
const WIDGET_EXAMPLE = JSON.stringify(
  [
    {
      slug: 'pushward-stats',
      name: 'PushWard',
      template: 'stat_list',
      interval: '60s',
      update_mode: 'on_change',
      stat_rows: [
        { label: 'Up targets', query: 'count(up == 1)', value_template: '{{ .Value }}' },
        { label: 'Total targets', query: 'count(up)', value_template: '{{ .Value }}' },
      ],
      content: { icon: 'chart.bar.fill', subtitle: 'Cluster health' },
    },
    {
      slug: 'pushward-http-5xx-rate',
      name: 'HTTP 5xx',
      template: 'gauge',
      query: 'vector(0)',
      interval: '1h',
      min_change: 0.05,
      content: { unit: 'req/s', severity: 'warning', min_value: 0, max_value: 2 },
    },
  ],
  null,
  2
);

type WidgetsMode = 'form' | 'json';

type State = AppPluginSettings & {
  // New API key value being entered (write-only; never read back from Grafana).
  apiKey: string;
  isApiKeySet: boolean;
  isWebhookTokenSet: boolean;
  // New end-to-end encryption key being entered (write-only, like apiKey).
  e2eKey: string;
  isE2EKeySet: boolean;
  // The saved key's Key ID and the backend's parse error for it, from /config.
  e2eKeyId?: string;
  e2eKeyError?: string;
  // Widget definitions. The form builder edits `widgets` directly; the JSON
  // editor edits `widgetsText`. widgetsMode selects which is the source of truth
  // on submit; switching modes serialises/parses between the two.
  widgets: WidgetConfig[];
  widgetsMode: WidgetsMode;
  // Raw text of the widgets JSON editor, plus the last client-side parse error.
  widgetsText: string;
  widgetsError?: string;
  // The backend's parse/validate error for the currently-saved config, fetched
  // on mount so a semantic error (duplicate slug, bad interval, unknown field)
  // the Go validator catches is shown here, not only on the Overview page.
  backendWidgetsError?: string;
};

export interface AppConfigProps extends PluginConfigPageProps<AppPluginMeta<AppPluginSettings>> {}

const AppConfig = ({ plugin }: AppConfigProps) => {
  const s = useStyles2(getStyles);
  const { enabled, pinned, jsonData, secureJsonFields } = plugin.meta;

  const [state, setState] = useState<State>({
    apiUrl: jsonData?.apiUrl ?? DEFAULTS.apiUrl,
    datasourceUid: jsonData?.datasourceUid ?? DEFAULTS.datasourceUid,
    severityLabel: jsonData?.severityLabel ?? DEFAULTS.severityLabel,
    defaultSeverity: jsonData?.defaultSeverity ?? DEFAULTS.defaultSeverity,
    priority: jsonData?.priority ?? DEFAULTS.priority,
    historyWindow: jsonData?.historyWindow ?? DEFAULTS.historyWindow,
    pollInterval: jsonData?.pollInterval ?? DEFAULTS.pollInterval,
    cleanupDelay: jsonData?.cleanupDelay ?? DEFAULTS.cleanupDelay,
    staleTimeout: jsonData?.staleTimeout ?? DEFAULTS.staleTimeout,
    smoothing: jsonData?.smoothing ?? DEFAULTS.smoothing,
    scale: jsonData?.scale ?? DEFAULTS.scale,
    decimals: jsonData?.decimals ?? DEFAULTS.decimals,
    alsoNotify: jsonData?.alsoNotify ?? DEFAULTS.alsoNotify,
    notifyLevel: jsonData?.notifyLevel ?? DEFAULTS.notifyLevel,
    ackEnabled: jsonData?.ackEnabled ?? DEFAULTS.ackEnabled,
    ackRepeatSeconds: jsonData?.ackRepeatSeconds ?? DEFAULTS.ackRepeatSeconds,
    ackExpireSeconds: jsonData?.ackExpireSeconds ?? DEFAULTS.ackExpireSeconds,
    apiKey: '',
    isApiKeySet: Boolean(secureJsonFields?.apiKey),
    isWebhookTokenSet: Boolean(secureJsonFields?.webhookToken),
    e2eKey: '',
    isE2EKeySet: Boolean(secureJsonFields?.e2eKey),
    widgets: jsonData?.widgets ?? [],
    widgetsMode: 'form',
    widgetsText: jsonData?.widgets?.length ? JSON.stringify(jsonData.widgets, null, 2) : '',
    widgetsError: undefined,
  });

  const isSubmitDisabled = !state.apiUrl;
  const isSilent = state.notifyLevel === 'passive';

  // Surface the backend's parse/validate result for the saved widget config, so
  // an error only the Go validator catches lands on the widgets editor here
  // rather than being discovered later on the Overview page. The saved
  // encryption key's Key ID and parse error come from the same call.
  // Best-effort.
  useEffect(() => {
    let active = true;
    getConfig()
      .then((cfg) => {
        if (!active || !(cfg.widgetsError || cfg.e2eKeyId || cfg.e2eError)) {
          return;
        }
        setState((prev) => ({
          ...prev,
          backendWidgetsError: cfg.widgetsError || prev.backendWidgetsError,
          e2eKeyId: cfg.e2eKeyId || undefined,
          e2eKeyError: cfg.e2eError
            ? `The saved key does not work, so alert pushes go out without their text: ${cfg.e2eError}`
            : undefined,
        }));
      })
      .catch(() => {
        /* non-fatal: the inline backend-error hint is best-effort */
      });
    return () => {
      active = false;
    };
  }, []);

  const onChangeText = (event: ChangeEvent<HTMLInputElement>) => {
    setState({ ...state, [event.target.name]: event.target.value });
  };

  // Whole numbers only: the backend reads these into ints, and a decimal would
  // fail the whole settings load.
  const onChangeNumber = (event: ChangeEvent<HTMLInputElement>) => {
    const parsed = Math.trunc(Number(event.target.value));
    setState({ ...state, [event.target.name]: Number.isNaN(parsed) ? 0 : parsed });
  };

  const onResetApiKey = () => setState({ ...state, apiKey: '', isApiKeySet: false });

  const onChangeE2EKey = (event: ChangeEvent<HTMLInputElement>) =>
    setState({ ...state, e2eKey: event.target.value, e2eKeyError: undefined });

  const onResetE2EKey = () =>
    setState({ ...state, e2eKey: '', isE2EKeySet: false, e2eKeyId: undefined, e2eKeyError: undefined });

  const onChangeWidgets = (event: ChangeEvent<HTMLTextAreaElement>) => {
    // Clear any stale parse error (client and backend) as the user edits.
    setState({ ...state, widgetsText: event.target.value, widgetsError: undefined, backendWidgetsError: undefined });
  };

  const onWidgetsChange = (widgets: WidgetConfig[]) =>
    setState((prev) => ({ ...prev, widgets, widgetsError: undefined, backendWidgetsError: undefined }));

  // parseWidgetsText turns the JSON editor's text into a widgets array, throwing
  // on anything that isn't a JSON array so callers can surface an inline error.
  const parseWidgetsText = (text: string): WidgetConfig[] => {
    const raw = text.trim();
    if (raw === '') {
      return [];
    }
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) {
      throw new Error('Widgets must be a JSON array.');
    }
    return parsed as WidgetConfig[];
  };

  const onSetWidgetsMode = (mode: WidgetsMode) => {
    if (mode === state.widgetsMode) {
      return;
    }
    if (mode === 'json') {
      // form -> json: serialise the current builder state into the editor.
      setState({
        ...state,
        widgetsMode: 'json',
        widgetsText: state.widgets.length ? JSON.stringify(state.widgets, null, 2) : '',
        widgetsError: undefined,
      });
      return;
    }
    // json -> form: parse, blocking the switch on invalid JSON.
    try {
      const widgets = parseWidgetsText(state.widgetsText);
      setState({ ...state, widgetsMode: 'form', widgets, widgetsError: undefined, backendWidgetsError: undefined });
    } catch (e) {
      setState({ ...state, widgetsError: e instanceof Error ? e.message : 'Invalid JSON.' });
    }
  };

  const onLoadWidgetExample = () => {
    const widgets = JSON.parse(WIDGET_EXAMPLE) as WidgetConfig[];
    setState({ ...state, widgets, widgetsText: WIDGET_EXAMPLE, widgetsError: undefined, backendWidgetsError: undefined });
  };

  const onSubmit = (event: SubmitEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (isSubmitDisabled) {
      return;
    }

    // In JSON mode parse the editor (blocking the save on malformed JSON); in
    // form mode the builder array is already the source of truth. Semantic
    // validation (slugs, intervals, required fields) stays with the backend,
    // which surfaces backendWidgetsError on the next load.
    let widgets: WidgetConfig[];
    if (state.widgetsMode === 'json') {
      try {
        widgets = parseWidgetsText(state.widgetsText);
      } catch (e) {
        setState({ ...state, widgetsError: e instanceof Error ? e.message : 'Invalid JSON.' });
        return;
      }
    } else {
      widgets = state.widgets;
    }

    // Grafana keeps every secure key a save leaves out, so send only the
    // secrets the user typed, plus an empty value for one they reset, which
    // removes it.
    const secureJsonData: Record<string, string> = {};
    if (state.apiKey !== '' || (secureJsonFields?.apiKey && !state.isApiKeySet)) {
      secureJsonData.apiKey = state.apiKey;
    }
    if (state.e2eKey.trim() !== '') {
      const { key, error } = checkE2EKey(state.e2eKey);
      if (error) {
        setState({ ...state, e2eKeyError: error });
        return;
      }
      secureJsonData.e2eKey = key;
    } else if (secureJsonFields?.e2eKey && !state.isE2EKeySet) {
      secureJsonData.e2eKey = '';
    }

    updatePluginAndReload(plugin.meta.id, {
      enabled,
      pinned,
      jsonData: {
        apiUrl: state.apiUrl,
        datasourceUid: state.datasourceUid,
        severityLabel: state.severityLabel,
        defaultSeverity: state.defaultSeverity,
        priority: state.priority,
        historyWindow: state.historyWindow,
        pollInterval: state.pollInterval,
        cleanupDelay: state.cleanupDelay,
        staleTimeout: state.staleTimeout,
        smoothing: state.smoothing,
        scale: state.scale,
        decimals: state.decimals,
        alsoNotify: state.alsoNotify,
        notifyLevel: state.notifyLevel,
        ackEnabled: state.ackEnabled,
        ackRepeatSeconds: state.ackRepeatSeconds,
        ackExpireSeconds: state.ackExpireSeconds,
        widgets,
      },
      secureJsonData: Object.keys(secureJsonData).length > 0 ? secureJsonData : undefined,
    });
  };

  return (
    <form onSubmit={onSubmit} data-testid={testIds.appConfig.container} className={s.form}>
      <div className={s.intro}>
        <Text color="secondary">
          Two-step setup: set your PushWard API key and history datasource here, then provision the webhook contact point
          on the{' '}
          <a className={s.link} href={CONNECT_HREF}>
            Connect
          </a>{' '}
          page.
        </Text>
      </div>

      <FieldSet label="PushWard API">
        <Field label="API key" description="Your PushWard integration key (hlk_…). Used as the Bearer token for api.pushward.app.">
          <SecretInput
            width={TEXT_WIDTH}
            id="config-api-key"
            data-testid={testIds.appConfig.apiKey}
            name="apiKey"
            value={state.apiKey}
            isConfigured={state.isApiKeySet}
            placeholder="hlk_…"
            onChange={onChangeText}
            onReset={onResetApiKey}
          />
        </Field>

        <Field
          label="End-to-end encryption key"
          description={
            <>
              Optional. Encrypts the title, subtitle and body of the push notifications this plugin sends, so only your
              devices holding the key can read them; a device without it, or on a PushWard app older than 1.17, shows a
              placeholder. Create it in the PushWard app under Settings &gt; End-to-End Encryption. Live Activities are
              not encrypted, and an organization&apos;s integration key cannot send encrypted notifications.
              {state.isE2EKeySet && state.e2eKeyId && (
                <>
                  {' '}
                  Key ID in use: <code data-testid={testIds.appConfig.e2eKeyId}>{state.e2eKeyId}</code> (the app shows
                  the same Key ID next to the key).
                </>
              )}
            </>
          }
          invalid={Boolean(state.e2eKeyError)}
          error={state.e2eKeyError}
        >
          <SecretInput
            width={TEXT_WIDTH}
            id="config-e2e-key"
            data-testid={testIds.appConfig.e2eKey}
            name="e2eKey"
            value={state.e2eKey}
            isConfigured={state.isE2EKeySet}
            placeholder="64 hexadecimal characters"
            onChange={onChangeE2EKey}
            onReset={onResetE2EKey}
          />
        </Field>

        <Field label="API URL" description="PushWard API base URL.">
          <Input
            width={TEXT_WIDTH}
            name="apiUrl"
            id="config-api-url"
            data-testid={testIds.appConfig.apiUrl}
            value={state.apiUrl}
            placeholder={DEFAULTS.apiUrl}
            onChange={onChangeText}
          />
        </Field>

        <Field
          label="Webhook token"
          description="The Grafana service-account token embedded in the PushWard contact point. Set this from the Connect page."
        >
          <Input
            width={TEXT_WIDTH}
            disabled
            value={state.isWebhookTokenSet ? 'Configured — managed by the Connect page' : 'Not set — use the Connect page'}
          />
        </Field>
      </FieldSet>

      <FieldSet label="History datasource">
        <Field
          label="Datasource"
          description="Prometheus / VictoriaMetrics datasource used to query alert history for the timeline."
        >
          <div data-testid={testIds.appConfig.datasource}>
            <DataSourcePicker
              current={state.datasourceUid || null}
              noDefault
              width={TEXT_WIDTH}
              filter={(ds: DataSourceInstanceSettings) =>
                ds.type === 'prometheus' || ds.type === 'victoriametrics-metrics-datasource'
              }
              onChange={(ds: DataSourceInstanceSettings) => setState({ ...state, datasourceUid: ds.uid })}
              onClear={() => setState({ ...state, datasourceUid: '' })}
            />
          </div>
        </Field>
      </FieldSet>

      <FieldSet label="Timeline behaviour">
        <Field label="Severity label" description="Alert label that carries severity.">
          <Input
            width={TEXT_WIDTH}
            name="severityLabel"
            data-testid={testIds.appConfig.severityLabel}
            value={state.severityLabel}
            placeholder={DEFAULTS.severityLabel}
            onChange={onChangeText}
          />
        </Field>

        <Field label="Default severity" description="Severity used when the label is absent.">
          <Input
            width={TEXT_WIDTH}
            name="defaultSeverity"
            data-testid={testIds.appConfig.defaultSeverity}
            value={state.defaultSeverity}
            placeholder={DEFAULTS.defaultSeverity}
            onChange={onChangeText}
          />
        </Field>

        <Field label="Priority" description="Live Activity priority (0–10).">
          <Input
            width={NUM_WIDTH}
            type="number"
            min={0}
            max={10}
            name="priority"
            data-testid={testIds.appConfig.priority}
            value={state.priority}
            onChange={onChangeNumber}
          />
        </Field>

        <Field label="History window" description="How far back to query history (Go duration, e.g. 30m).">
          <Input
            width={NUM_WIDTH}
            name="historyWindow"
            data-testid={testIds.appConfig.historyWindow}
            value={state.historyWindow}
            placeholder={DEFAULTS.historyWindow}
            onChange={onChangeText}
          />
        </Field>

        <Field
          label="Also send a push notification"
          description="Also deliver a normal push notification (banner / Lock Screen) when an alert fires and resolves, in addition to the timeline Live Activity."
        >
          <Switch
            data-testid={testIds.appConfig.alsoNotify}
            value={state.alsoNotify}
            onChange={(e) => setState({ ...state, alsoNotify: e.currentTarget.checked })}
          />
        </Field>

        {state.alsoNotify && (
          <>
            <Field
              label="Notification priority"
              description="How intrusive the push is (fire and resolve both use this). Silent: quiet, Lock Screen only. Normal: alerts as usual. Critical: breaks through Focus / silent mode (needs the critical-alert entitlement on your PushWard account, otherwise delivered as time-sensitive)."
            >
              <RadioButtonGroup
                data-testid={testIds.appConfig.notifyLevel}
                options={NOTIFY_LEVEL_OPTIONS}
                value={state.notifyLevel}
                onChange={(v) => setState({ ...state, notifyLevel: v ?? DEFAULTS.notifyLevel })}
              />
            </Field>

            <Field
              label="Repeat until acknowledged"
              description={
                isSilent
                  ? 'Not available at Silent priority: a silent push never alerts, so there is nothing to acknowledge.'
                  : 'Repeat the firing push until someone taps Acknowledge on a device, or until it stops on its own. Resolving the alert stops the repeats. At most 25 notifications can repeat at once per account; past that the alert is sent once without repeating.'
              }
              disabled={isSilent}
            >
              <Switch
                data-testid={testIds.appConfig.ackEnabled}
                value={state.ackEnabled}
                disabled={isSilent}
                onChange={(e) => setState({ ...state, ackEnabled: e.currentTarget.checked })}
              />
            </Field>

            {state.ackEnabled && (
              <>
                <Field label="Repeat every" description="Seconds between repeats, 30 to 3600." disabled={isSilent}>
                  <Input
                    width={NUM_WIDTH}
                    type="number"
                    min={30}
                    max={3600}
                    name="ackRepeatSeconds"
                    data-testid={testIds.appConfig.ackRepeat}
                    value={state.ackRepeatSeconds}
                    disabled={isSilent}
                    onChange={onChangeNumber}
                  />
                </Field>

                <Field
                  label="Stop repeating after"
                  description="Seconds after which an unacknowledged push stops repeating, 60 to 10800 (3 hours)."
                  disabled={isSilent}
                >
                  <Input
                    width={NUM_WIDTH}
                    type="number"
                    min={60}
                    max={10800}
                    name="ackExpireSeconds"
                    data-testid={testIds.appConfig.ackExpire}
                    value={state.ackExpireSeconds}
                    disabled={isSilent}
                    onChange={onChangeNumber}
                  />
                </Field>
              </>
            )}
          </>
        )}

        <ControlledCollapse label="Advanced timeline options" isOpen={false}>
          <Field label="Poll interval" description="How often firing alerts are re-queried (e.g. 30s).">
            <Input
              width={NUM_WIDTH}
              name="pollInterval"
              data-testid={testIds.appConfig.pollInterval}
              value={state.pollInterval}
              placeholder={DEFAULTS.pollInterval}
              onChange={onChangeText}
            />
          </Field>

          <Field label="Cleanup delay" description="Delay before a resolved activity is ended (e.g. 15m).">
            <Input
              width={NUM_WIDTH}
              name="cleanupDelay"
              data-testid={testIds.appConfig.cleanupDelay}
              value={state.cleanupDelay}
              placeholder={DEFAULTS.cleanupDelay}
              onChange={onChangeText}
            />
          </Field>

          <Field label="Stale timeout" description="Max activity lifetime before it is force-ended (e.g. 24h).">
            <Input
              width={NUM_WIDTH}
              name="staleTimeout"
              data-testid={testIds.appConfig.staleTimeout}
              value={state.staleTimeout}
              placeholder={DEFAULTS.staleTimeout}
              onChange={onChangeText}
            />
          </Field>

          <Field label="Scale" description="Y-axis scale for the timeline chart.">
            <div data-testid={testIds.appConfig.scale}>
              <Combobox
                width={TEXT_WIDTH}
                options={SCALE_OPTIONS}
                value={state.scale}
                onChange={(opt: ComboboxOption<string>) => setState({ ...state, scale: opt.value })}
              />
            </div>
          </Field>

          <Field label="Decimals" description="Decimal places shown for values.">
            <Input
              width={NUM_WIDTH}
              type="number"
              min={0}
              name="decimals"
              data-testid={testIds.appConfig.decimals}
              value={state.decimals}
              onChange={onChangeNumber}
            />
          </Field>

          <Field label="Smoothing" description="Smooth the timeline chart line.">
            <Switch
              data-testid={testIds.appConfig.smoothing}
              value={state.smoothing}
              onChange={(e) => setState({ ...state, smoothing: e.currentTarget.checked })}
            />
          </Field>
        </ControlledCollapse>
      </FieldSet>

      <FieldSet label="Widgets">
        <Field
          label="Editor"
          description={
            'Widgets published to PushWard as PromQL-backed iOS widgets. Publishing requires the integration key to ' +
            'have the "widgets" scope and a datasource selected above.'
          }
          invalid={Boolean(state.widgetsError || state.backendWidgetsError)}
          error={state.widgetsError ?? state.backendWidgetsError}
        >
          <RadioButtonGroup<WidgetsMode>
            options={[
              { label: 'Form', value: 'form' },
              { label: 'JSON', value: 'json' },
            ]}
            value={state.widgetsMode}
            onChange={onSetWidgetsMode}
          />
        </Field>

        {state.widgetsMode === 'form' ? (
          <WidgetBuilder widgets={state.widgets} onChange={onWidgetsChange} />
        ) : (
          <>
            <TextArea
              id="config-widgets"
              data-testid={testIds.appConfig.widgets}
              className={s.code}
              rows={12}
              value={state.widgetsText}
              placeholder='[ { "slug": "my-widget", "name": "My widget", "template": "stat_list", "stat_rows": [] } ]'
              onChange={onChangeWidgets}
            />
            <div className={s.marginTop}>
              <Button
                type="button"
                variant="secondary"
                icon="file-alt"
                data-testid={testIds.appConfig.widgetsExample}
                onClick={onLoadWidgetExample}
              >
                Load example
              </Button>
            </div>
          </>
        )}
      </FieldSet>

      <div className={s.footer}>
        <Button type="submit" data-testid={testIds.appConfig.submit} disabled={isSubmitDisabled}>
          Save settings
        </Button>
      </div>
    </form>
  );
};

export default AppConfig;

const getStyles = (theme: GrafanaTheme2) => ({
  form: css`
    max-width: 760px;
  `,
  intro: css`
    margin-bottom: ${theme.spacing(3)};
  `,
  link: css`
    color: ${theme.colors.text.link};
    text-decoration: underline;
  `,
  marginTop: css`
    margin-top: ${theme.spacing(2)};
  `,
  footer: css`
    margin-top: ${theme.spacing(3)};
  `,
  code: css`
    font-family: ${theme.typography.fontFamilyMonospace};
    font-size: ${theme.typography.bodySmall.fontSize};
  `,
});

const updatePluginAndReload = async (pluginId: string, data: Partial<PluginMeta<AppPluginSettings>>) => {
  try {
    await updatePlugin(pluginId, data);

    // Reload so the new settings propagate to the running plugin instance.
    window.location.reload();
  } catch (e) {
    // The error carries the request, secrets included, so log only what failed.
    console.error('Error while updating the plugin', (e as { status?: number })?.status, errorMessage(e));
  }
};

const updatePlugin = async (pluginId: string, data: Partial<PluginMeta>) => {
  const response = getBackendSrv().fetch({
    url: `/api/plugins/${pluginId}/settings`,
    method: 'POST',
    data,
  });

  return lastValueFrom(response);
};
