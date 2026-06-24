import React, { ChangeEvent, FormEvent, useState } from 'react';
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
  Field,
  FieldSet,
  Input,
  SecretInput,
  Switch,
  useStyles2,
  type ComboboxOption,
} from '@grafana/ui';
import { testIds } from '../testIds';

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
};

const SCALE_OPTIONS: Array<ComboboxOption<string>> = [
  { label: 'Linear', value: 'linear' },
  { label: 'Logarithmic', value: 'logarithmic' },
];

type State = AppPluginSettings & {
  // New API key value being entered (write-only; never read back from Grafana).
  apiKey: string;
  isApiKeySet: boolean;
  isWebhookTokenSet: boolean;
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
    apiKey: '',
    isApiKeySet: Boolean(secureJsonFields?.apiKey),
    isWebhookTokenSet: Boolean(secureJsonFields?.webhookToken),
  });

  const isSubmitDisabled = !state.apiUrl;

  const onChangeText = (event: ChangeEvent<HTMLInputElement>) => {
    setState({ ...state, [event.target.name]: event.target.value });
  };

  const onChangeNumber = (event: ChangeEvent<HTMLInputElement>) => {
    const parsed = Number(event.target.value);
    setState({ ...state, [event.target.name]: Number.isNaN(parsed) ? 0 : parsed });
  };

  const onResetApiKey = () => setState({ ...state, apiKey: '', isApiKeySet: false });

  const onSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (isSubmitDisabled) {
      return;
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
      },
      // Only send the secret when the user typed a new one — never overwrite a
      // previously-stored key with an empty value.
      secureJsonData: state.isApiKeySet ? undefined : { apiKey: state.apiKey },
    });
  };

  return (
    <form onSubmit={onSubmit} data-testid={testIds.appConfig.container}>
      <FieldSet label="PushWard API">
        <Field label="API key" description="Your PushWard integration key (hlk_…). Used as the Bearer token for api.pushward.app.">
          <SecretInput
            width={60}
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

        <Field label="API URL" description="PushWard API base URL.">
          <Input
            width={60}
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
            width={60}
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
              width={40}
              filter={(ds: DataSourceInstanceSettings) =>
                ds.type === 'prometheus' || ds.type.includes('victoriametrics')
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
            width={40}
            name="severityLabel"
            data-testid={testIds.appConfig.severityLabel}
            value={state.severityLabel}
            placeholder={DEFAULTS.severityLabel}
            onChange={onChangeText}
          />
        </Field>

        <Field label="Default severity" description="Severity used when the label is absent.">
          <Input
            width={40}
            name="defaultSeverity"
            data-testid={testIds.appConfig.defaultSeverity}
            value={state.defaultSeverity}
            placeholder={DEFAULTS.defaultSeverity}
            onChange={onChangeText}
          />
        </Field>

        <Field label="Priority" description="Live Activity priority (0–10).">
          <Input
            width={20}
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
            width={20}
            name="historyWindow"
            data-testid={testIds.appConfig.historyWindow}
            value={state.historyWindow}
            placeholder={DEFAULTS.historyWindow}
            onChange={onChangeText}
          />
        </Field>

        <Field label="Poll interval" description="How often firing alerts are re-queried (e.g. 30s).">
          <Input
            width={20}
            name="pollInterval"
            data-testid={testIds.appConfig.pollInterval}
            value={state.pollInterval}
            placeholder={DEFAULTS.pollInterval}
            onChange={onChangeText}
          />
        </Field>

        <Field label="Cleanup delay" description="Delay before a resolved activity is ended (e.g. 15m).">
          <Input
            width={20}
            name="cleanupDelay"
            data-testid={testIds.appConfig.cleanupDelay}
            value={state.cleanupDelay}
            placeholder={DEFAULTS.cleanupDelay}
            onChange={onChangeText}
          />
        </Field>

        <Field label="Stale timeout" description="Max activity lifetime before it is force-ended (e.g. 24h).">
          <Input
            width={20}
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
              width={30}
              options={SCALE_OPTIONS}
              value={state.scale}
              onChange={(opt: ComboboxOption<string>) => setState({ ...state, scale: opt.value })}
            />
          </div>
        </Field>

        <Field label="Decimals" description="Decimal places shown for values.">
          <Input
            width={20}
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
      </FieldSet>

      <div className={s.marginTop}>
        <Button type="submit" data-testid={testIds.appConfig.submit} disabled={isSubmitDisabled}>
          Save settings
        </Button>
      </div>
    </form>
  );
};

export default AppConfig;

const getStyles = (theme: GrafanaTheme2) => ({
  marginTop: css`
    margin-top: ${theme.spacing(3)};
  `,
});

const updatePluginAndReload = async (pluginId: string, data: Partial<PluginMeta<AppPluginSettings>>) => {
  try {
    await updatePlugin(pluginId, data);

    // Reload so the new settings propagate to the running plugin instance.
    window.location.reload();
  } catch (e) {
    console.error('Error while updating the plugin', e);
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
