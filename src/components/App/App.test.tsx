import React, { Suspense } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { AppRootProps, PluginType } from '@grafana/data';
import { render, waitFor } from '@testing-library/react';
import App from './App';
import { testIds } from '../testIds';

// Route the Overview page's resource reads by URL so a test can fail one leg
// independently. The default returns healthy payloads for every route.
const healthzConfig = {
  ok: true,
  apiKey: true,
  apiKeyStatus: 'valid',
  datasource: true,
  history: true,
  widgets: false,
  widgetsError: '',
  message: 'ok',
  apiKeySet: true,
  webhookConnected: true,
  widgetCount: 0,
};
const statsPayload = { alertsReceived: 12, activitiesCreated: 3, pushesSent: 9, errors: 0 };

let getMock: jest.Mock;

// PluginPage is a no-op wrapper in tests; the resource calls are stubbed so the
// Overview page renders its shell without hitting a backend.
jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  PluginPage: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  getBackendSrv: () => ({
    get: getMock,
    post: jest.fn().mockResolvedValue({ ok: true, message: '' }),
  }),
}));

function routedGet(url: string): Promise<unknown> {
  if (url.endsWith('/stats')) {
    return Promise.resolve(statsPayload);
  }
  return Promise.resolve(healthzConfig);
}

// Pages are lazy-loaded, and the first render pays for jest's on-demand
// transform of the whole Overview module graph. That routinely runs past jest's
// 5s default, so both tests get explicit headroom.
const RENDER_TIMEOUT_MS = 20000;

describe('Components/App', () => {
  let props: AppRootProps;

  beforeEach(() => {
    getMock = jest.fn(routedGet);

    props = {
      basename: 'a/pushward-alerts-app',
      meta: {
        id: 'pushward-alerts-app',
        name: 'PushWard',
        type: PluginType.app,
        enabled: true,
        jsonData: {},
      },
      query: {},
      path: '',
      onNavChanged: jest.fn(),
    } as unknown as AppRootProps;
  });

  test(
    'renders the Overview page by default',
    async () => {
      const { queryByTestId } = render(
        <MemoryRouter>
          <Suspense fallback={null}>
            <App {...props} />
          </Suspense>
        </MemoryRouter>
      );

      await waitFor(() => expect(queryByTestId(testIds.overview.container)).toBeInTheDocument(), {
        timeout: RENDER_TIMEOUT_MS / 2,
      });
    },
    RENDER_TIMEOUT_MS
  );

  test(
    'renders the delivery counters once the resource calls resolve',
    async () => {
      const { findByTestId, findByText } = render(
        <MemoryRouter>
          <Suspense fallback={null}>
            <App {...props} />
          </Suspense>
        </MemoryRouter>
      );

      await findByTestId(testIds.overview.stats, undefined, { timeout: RENDER_TIMEOUT_MS / 2 });
      expect(await findByText('12')).toBeInTheDocument();
      expect(await findByText('Alerts received')).toBeInTheDocument();
    },
    RENDER_TIMEOUT_MS
  );

  test(
    'still renders the page when the stats read fails',
    async () => {
      getMock = jest.fn((url: string) =>
        url.endsWith('/stats') ? Promise.reject(new Error('no /stats route')) : routedGet(url)
      );

      const { findByTestId, queryByTestId } = render(
        <MemoryRouter>
          <Suspense fallback={null}>
            <App {...props} />
          </Suspense>
        </MemoryRouter>
      );

      // The status section still loads; only the delivery grid is absent.
      await findByTestId(testIds.overview.status, undefined, { timeout: RENDER_TIMEOUT_MS / 2 });
      expect(queryByTestId(testIds.overview.stats)).not.toBeInTheDocument();
    },
    RENDER_TIMEOUT_MS
  );
});
