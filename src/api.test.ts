import { connectToAlerting, errorMessage, getActivities, getWidgets, webhookUrl } from './api';
import { config, getBackendSrv } from '@grafana/runtime';
import { of } from 'rxjs';

// api.ts only touches `config` and `getBackendSrv`; stub the rest of the module.
jest.mock('@grafana/runtime', () => ({
  config: { appUrl: 'http://localhost:3000/' },
  getBackendSrv: jest.fn(),
}));

const mockBackendSrv = getBackendSrv as jest.MockedFunction<typeof getBackendSrv>;

function stubGet(value: unknown) {
  const get = jest.fn().mockResolvedValue(value);
  mockBackendSrv.mockReturnValue({ get } as unknown as ReturnType<typeof getBackendSrv>);
  return get;
}

// Wires up the full set of Grafana API calls connectToAlerting makes, recording
// every fetch() body so a test can assert what the settings POST sent.
function stubConnectBackend(currentJsonData: Record<string, unknown>) {
  const fetchCalls: Array<{ url: string; data: Record<string, unknown> }> = [];
  const get = jest.fn((url: string) => {
    if (url === '/api/serviceaccounts/search') {
      return Promise.resolve({ serviceAccounts: [] });
    }
    if (url === '/api/plugins/pushward-alerts-app/settings') {
      return Promise.resolve({ jsonData: currentJsonData });
    }
    if (url === '/api/v1/provisioning/contact-points') {
      return Promise.resolve([]);
    }
    if (/\/tokens$/.test(url)) {
      return Promise.resolve([]); // revoke-cleanup list
    }
    return Promise.resolve({});
  });
  const post = jest.fn((url: string) => {
    if (url === '/api/serviceaccounts') {
      return Promise.resolve({ id: 1, name: 'pushward-alerts-webhook' });
    }
    if (/\/tokens$/.test(url)) {
      return Promise.resolve({ id: 99, name: 'pushward-alerts-webhook-1', key: 'glsa_token' });
    }
    return Promise.resolve({});
  });
  const fetch = jest.fn((opts: { url: string; data: Record<string, unknown> }) => {
    fetchCalls.push({ url: opts.url, data: opts.data });
    return of({ data: {} });
  });
  const del = jest.fn().mockResolvedValue({});
  mockBackendSrv.mockReturnValue({ get, post, fetch, delete: del } as unknown as ReturnType<typeof getBackendSrv>);
  return { fetchCalls };
}

describe('api', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    config.appUrl = 'http://localhost:3000/';
  });

  describe('getActivities', () => {
    it('returns the backend array', async () => {
      stubGet({ activities: [{ slug: 'a' }, { slug: 'b' }] });
      const res = await getActivities();
      expect(Array.isArray(res.activities)).toBe(true);
      expect(res.activities).toHaveLength(2);
    });

    it('defaults to an empty array when the payload is missing activities', async () => {
      stubGet({});
      const res = await getActivities();
      expect(Array.isArray(res.activities)).toBe(true);
      expect(res.activities).toEqual([]);
    });
  });

  describe('getWidgets', () => {
    it('defaults to an empty array when the payload is missing widgets', async () => {
      stubGet({});
      const res = await getWidgets();
      expect(Array.isArray(res.widgets)).toBe(true);
      expect(res.widgets).toEqual([]);
    });
  });

  describe('connectToAlerting', () => {
    it('preserves existing jsonData when saving the webhook token', async () => {
      const current = { datasourceUid: 'ds1', widgets: [{ slug: 'w' }], severityLabel: 'severity' };
      const { fetchCalls } = stubConnectBackend(current);

      await connectToAlerting(true, false);

      const settingsPost = fetchCalls.find((c) => c.url === '/api/plugins/pushward-alerts-app/settings');
      expect(settingsPost).toBeDefined();
      // The whole saved jsonData must be resent unchanged, not wiped.
      expect(settingsPost!.data.jsonData).toEqual(current);
      expect(settingsPost!.data.secureJsonData).toEqual({ webhookToken: 'glsa_token' });
      expect(settingsPost!.data.enabled).toBe(true);
    });
  });

  describe('errorMessage', () => {
    it('uses Error.message', () => {
      expect(errorMessage(new Error('boom'))).toBe('boom');
    });

    it('unwraps a getBackendSrv response object via data.message', () => {
      expect(errorMessage({ data: { message: 'bad request' } })).toBe('bad request');
    });

    it('falls back to statusText', () => {
      expect(errorMessage({ status: 500, statusText: 'Internal Server Error' })).toBe('Internal Server Error');
    });

    it('prefers data.message over statusText', () => {
      expect(errorMessage({ data: { message: 'detail' }, statusText: 'Bad Request' })).toBe('detail');
    });

    it('stringifies anything else', () => {
      expect(errorMessage('plain')).toBe('plain');
      expect(errorMessage(null)).toBe('null');
      expect(errorMessage({})).toBe('[object Object]');
    });
  });

  describe('webhookUrl', () => {
    it('preserves a configured Grafana sub-path', () => {
      config.appUrl = 'https://example.com/grafana/';
      expect(webhookUrl()).toBe(
        'https://example.com/grafana/api/plugins/pushward-alerts-app/resources/webhook'
      );
    });

    it('does not emit a double slash when appUrl has no sub-path', () => {
      config.appUrl = 'https://example.com/';
      expect(webhookUrl()).toBe('https://example.com/api/plugins/pushward-alerts-app/resources/webhook');
    });
  });
});
