import React from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { PluginType } from '@grafana/data';
import AppConfig, { AppConfigProps } from './AppConfig';
import { testIds } from 'components/testIds';

// DataSourcePicker reaches for getDataSourceSrv() on mount, so stub it (and
// getBackendSrv, used on submit + the mount effect that fetches /config) to keep
// the config form a pure render in tests.
jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  DataSourcePicker: () => <div data-testid="mock-datasource-picker" />,
  getBackendSrv: () => ({
    fetch: jest.fn(),
    get: jest.fn().mockResolvedValue({}),
  }),
}));

describe('Components/AppConfig', () => {
  let props: AppConfigProps;

  beforeAll(() => {
    // jsdom has no canvas; Combobox measures text width to auto-size itself.
    HTMLCanvasElement.prototype.getContext = jest
      .fn()
      .mockReturnValue({ measureText: () => ({ width: 0 }) }) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  });

  beforeEach(() => {
    jest.clearAllMocks();

    props = {
      plugin: {
        meta: {
          id: 'pushward-alerts-app',
          name: 'PushWard',
          type: PluginType.app,
          enabled: true,
          jsonData: {},
        },
      },
      query: {},
    } as unknown as AppConfigProps;
  });

  test('renders the config form with key fields and a save button', () => {
    const plugin = { meta: { ...props.plugin.meta, enabled: false } };

    // @ts-expect-error - addConfigPage()/setChannelSupport() aren't needed for this test
    render(<AppConfig plugin={plugin} query={props.query} />);

    expect(screen.queryByRole('group', { name: /pushward api/i })).toBeInTheDocument();
    expect(screen.queryByTestId(testIds.appConfig.apiKey)).toBeInTheDocument();
    expect(screen.queryByTestId(testIds.appConfig.apiUrl)).toBeInTheDocument();
    expect(screen.queryByTestId(testIds.appConfig.datasource)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /save settings/i })).toBeInTheDocument();

    // Scale and smoothing live under the collapsed "Advanced timeline options"
    // section: absent until the section is expanded, present after.
    expect(screen.queryByTestId(testIds.appConfig.scale)).not.toBeInTheDocument();
    expect(screen.queryByTestId(testIds.appConfig.smoothing)).not.toBeInTheDocument();
    fireEvent.click(screen.getByText(/advanced timeline options/i));
    expect(screen.queryByTestId(testIds.appConfig.scale)).toBeInTheDocument();
    expect(screen.queryByTestId(testIds.appConfig.smoothing)).toBeInTheDocument();
  });
});
