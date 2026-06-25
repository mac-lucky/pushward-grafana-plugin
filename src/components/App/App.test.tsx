import React, { Suspense } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { AppRootProps, PluginType } from '@grafana/data';
import { render, waitFor } from '@testing-library/react';
import App from './App';
import { testIds } from '../testIds';

// PluginPage is a no-op wrapper in tests; the resource calls are stubbed so the
// Overview page renders its shell without hitting a backend.
jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  PluginPage: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  getBackendSrv: () => ({
    // One object satisfies both /healthz and /config reads on Overview.
    get: jest.fn().mockResolvedValue({
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
    }),
    post: jest.fn().mockResolvedValue({ ok: true, message: '' }),
  }),
}));

describe('Components/App', () => {
  let props: AppRootProps;

  beforeEach(() => {
    jest.clearAllMocks();

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

  test('renders the Overview page by default', async () => {
    const { queryByTestId } = render(
      <MemoryRouter>
        <Suspense fallback={null}>
          <App {...props} />
        </Suspense>
      </MemoryRouter>
    );

    // Pages are lazy-loaded, so wait for the Overview container to appear.
    await waitFor(() => expect(queryByTestId(testIds.overview.container)).toBeInTheDocument(), { timeout: 2000 });
  });
});
