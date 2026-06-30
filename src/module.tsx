import React, { Suspense, lazy } from 'react';
import { AppPlugin, PluginExtensionPoints, type AppRootProps } from '@grafana/data';
import { LoadingPlaceholder } from '@grafana/ui';
import type { AppConfigProps } from './components/AppConfig/AppConfig';
import { PARAM_ALERTNAME, PARAM_EXPR, PARAM_RULE_UID, PLUGIN_BASE_URL, ROUTES } from './constants';

const LazyApp = lazy(() => import('./components/App/App'));
const LazyAppConfig = lazy(() => import('./components/AppConfig/AppConfig'));

const App = (props: AppRootProps) => (
  <Suspense fallback={<LoadingPlaceholder text="" />}>
    <LazyApp {...props} />
  </Suspense>
);

const AppConfig = (props: AppConfigProps) => (
  <Suspense fallback={<LoadingPlaceholder text="" />}>
    <LazyAppConfig {...props} />
  </Suspense>
);

// The alerting extension context isn't strongly typed across Grafana versions,
// so pull the rule identity defensively from the spots it has lived. Missing
// fields just mean the link falls back to its un-parameterized path.
type AlertExtensionContext = {
  alertName?: string;
  ruleUid?: string;
  expr?: string;
};

function readAlertContext(context?: Record<string, unknown>): AlertExtensionContext {
  if (!context || typeof context !== 'object') {
    return {};
  }
  const c = context as Record<string, any>;
  const rule = c.rule ?? c.alertRule ?? {};
  const labels = c.labels ?? rule.labels ?? c.instance?.labels ?? {};
  const expr =
    c.expr ?? rule.expr ?? rule.query ?? (Array.isArray(rule.data) ? rule.data[0]?.model?.expr : undefined);
  return {
    alertName: labels.alertname ?? c.alertname ?? rule.title ?? undefined,
    ruleUid: rule.uid ?? c.ruleUid ?? c.ruleUID ?? undefined,
    expr: typeof expr === 'string' ? expr : undefined,
  };
}

function withParams(base: string, params: Record<string, string | undefined>): string {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v) {
      qs.set(k, v);
    }
  }
  const s = qs.toString();
  return s ? `${base}?${s}` : base;
}

export const plugin = new AppPlugin<{}>()
  .setRootPage(App)
  .addConfigPage({
    title: 'Configuration',
    icon: 'cog',
    body: AppConfig,
    id: 'configuration',
  })
  // "View in PushWard" - opens the Activities page, filtered to the matching
  // alert when the extension context carries one.
  .addLink({
    targets: [PluginExtensionPoints.AlertingAlertingRuleAction, PluginExtensionPoints.AlertInstanceAction],
    title: 'View in PushWard',
    description: 'Open the matching Live Activities and delivery log in PushWard.',
    icon: 'bell',
    path: `${PLUGIN_BASE_URL}/${ROUTES.Activities}`,
    configure: (context?: Record<string, unknown>) => {
      const { alertName, ruleUid } = readAlertContext(context);
      return {
        path: withParams(`${PLUGIN_BASE_URL}/${ROUTES.Activities}`, {
          [PARAM_ALERTNAME]: alertName,
          [PARAM_RULE_UID]: ruleUid,
        }),
      };
    },
  })
  // "Set up PushWard for this alert" - jumps to the Connect page with the rule's
  // PromQL pre-filled so the user can wire the timeline in one step.
  .addLink({
    targets: [PluginExtensionPoints.AlertingAlertingRuleAction],
    title: 'Set up PushWard for this alert',
    description: 'Pre-fill the pushward_query annotation so this alert streams a timeline Live Activity.',
    icon: 'plus-circle',
    path: `${PLUGIN_BASE_URL}/${ROUTES.Connect}`,
    configure: (context?: Record<string, unknown>) => {
      const { expr, ruleUid } = readAlertContext(context);
      return {
        path: withParams(`${PLUGIN_BASE_URL}/${ROUTES.Connect}`, {
          [PARAM_EXPR]: expr,
          [PARAM_RULE_UID]: ruleUid,
        }),
      };
    },
  });
