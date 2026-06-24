import React, { Suspense, lazy } from 'react';
import { AppPlugin, PluginExtensionPoints, type AppRootProps } from '@grafana/data';
import { LoadingPlaceholder } from '@grafana/ui';
import type { AppConfigProps } from './components/AppConfig/AppConfig';
import { PLUGIN_BASE_URL, ROUTES } from './constants';

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

export const plugin = new AppPlugin<{}>()
  .setRootPage(App)
  .addConfigPage({
    title: 'Configuration',
    icon: 'cog',
    body: AppConfig,
    id: 'configuration',
  })
  // Surface a "View in PushWard" link from the alert-rule and alert-instance
  // action menus that deep-links into this plugin's Activities page.
  .addLink({
    targets: [PluginExtensionPoints.AlertingAlertingRuleAction, PluginExtensionPoints.AlertInstanceAction],
    title: 'View in PushWard',
    description: 'Open the matching Live Activities and delivery log in PushWard.',
    icon: 'bell',
    path: `${PLUGIN_BASE_URL}/${ROUTES.Activities}`,
  });
