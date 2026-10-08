import React from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { PluginType } from '@grafana/data';
import AppConfig, { AppConfigProps } from './AppConfig';
import { testIds } from 'components/testIds';

// DataSourcePicker reaches for getDataSourceSrv() on mount, so stub it (and
// getBackendSrv, used on submit + the mount effect that fetches /config) to keep
// the config form a pure render in tests.
const mockGet = jest.fn();
jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  DataSourcePicker: () => <div data-testid="mock-datasource-picker" />,
  getBackendSrv: () => ({
    fetch: jest.fn(),
    get: mockGet,
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
    mockGet.mockResolvedValue({});

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

  test('renders the encryption key field and the Key ID of the saved key', async () => {
    mockGet.mockResolvedValue({ e2eKeyId: '1a2b3c4d', e2eError: '' });
    const plugin = { meta: { ...props.plugin.meta, secureJsonFields: { e2eKey: true } } };

    // @ts-expect-error - addConfigPage()/setChannelSupport() aren't needed for this test
    render(<AppConfig plugin={plugin} query={props.query} />);

    expect(screen.getByTestId(testIds.appConfig.e2eKey)).toBeInTheDocument();
    expect(await screen.findByTestId(testIds.appConfig.e2eKeyId)).toHaveTextContent('1a2b3c4d');
  });

  test('shows the backend error for a saved key that does not parse', async () => {
    mockGet.mockResolvedValue({ e2eKeyId: '', e2eError: 'an encryption key is 64 hex characters, got 8' });
    const plugin = { meta: { ...props.plugin.meta, secureJsonFields: { e2eKey: true } } };

    // @ts-expect-error - addConfigPage()/setChannelSupport() aren't needed for this test
    render(<AppConfig plugin={plugin} query={props.query} />);

    expect(await screen.findByText(/got 8/)).toBeInTheDocument();
    expect(screen.queryByTestId(testIds.appConfig.e2eKeyId)).not.toBeInTheDocument();
  });

  test('acknowledge controls follow the notification switch and are disabled at Silent', () => {
    const { unmount } = render(<AppConfig plugin={props.plugin} query={props.query} />);
    expect(screen.queryByTestId(testIds.appConfig.ackEnabled)).not.toBeInTheDocument();
    unmount();

    const on = {
      meta: { ...props.plugin.meta, jsonData: { alsoNotify: true, ackEnabled: true, ackRepeatSeconds: 120 } },
    };
    // @ts-expect-error - addConfigPage()/setChannelSupport() aren't needed for this test
    const second = render(<AppConfig plugin={on} query={props.query} />);
    expect(screen.getByTestId(testIds.appConfig.ackEnabled)).toBeEnabled();
    expect(screen.getByTestId(testIds.appConfig.ackRepeat)).toHaveValue(120);
    expect(screen.getByTestId(testIds.appConfig.ackExpire)).toHaveValue(3600);
    second.unmount();

    const silent = { meta: { ...on.meta, jsonData: { ...on.meta.jsonData, notifyLevel: 'passive' } } };
    // @ts-expect-error - addConfigPage()/setChannelSupport() aren't needed for this test
    render(<AppConfig plugin={silent} query={props.query} />);
    expect(screen.getByTestId(testIds.appConfig.ackEnabled)).toBeDisabled();
    expect(screen.getByTestId(testIds.appConfig.ackRepeat)).toBeDisabled();
    expect(screen.getByTestId(testIds.appConfig.ackExpire)).toBeDisabled();
    expect(screen.getByText(/not available at silent priority/i)).toBeInTheDocument();
  });

  test('number fields keep whole numbers only', () => {
    const plugin = { meta: { ...props.plugin.meta, jsonData: { alsoNotify: true, ackEnabled: true } } };
    // @ts-expect-error - addConfigPage()/setChannelSupport() aren't needed for this test
    render(<AppConfig plugin={plugin} query={props.query} />);

    const repeat = screen.getByTestId(testIds.appConfig.ackRepeat);
    fireEvent.change(repeat, { target: { value: '45.7' } });
    expect(repeat).toHaveValue(45);

    const priority = screen.getByTestId(testIds.appConfig.priority);
    fireEvent.change(priority, { target: { value: '7.5' } });
    expect(priority).toHaveValue(7);
  });
});
