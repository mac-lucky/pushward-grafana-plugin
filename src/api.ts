import { lastValueFrom } from 'rxjs';
import { getBackendSrv } from '@grafana/runtime';
import { PLUGIN_ID, RESOURCE_BASE_URL } from './constants';

// ---------------------------------------------------------------------------
// Backend resource responses (see the Go backend's /resources routes).
// ---------------------------------------------------------------------------

export interface HealthzResponse {
  ok: boolean;
  apiKey: boolean;
  datasource: boolean;
  message: string;
}

export interface ConfigResponse {
  apiUrl: string;
  datasourceUid: string;
  severityLabel: string;
  defaultSeverity: string;
  priority: number;
  historyWindow: string;
  pollInterval: string;
  cleanupDelay: string;
  staleTimeout: string;
  smoothing: boolean;
  scale: string;
  decimals: number;
  apiKeySet: boolean;
  webhookConnected: boolean;
  webhookUrl: string;
}

export interface TestResponse {
  ok: boolean;
  message: string;
}

// Raw passthrough of PushWard's GET /activities — kept loose on purpose.
export interface ActivitySummary {
  slug?: string;
  name?: string;
  state?: string;
  priority?: number;
  template?: string;
  created_at?: string;
  updated_at?: string;
  [key: string]: unknown;
}

export interface ActivitiesResponse {
  activities: ActivitySummary[];
}

export interface HistoryEntry {
  ts: number;
  alertname: string;
  slug: string;
  action: string;
  ok: boolean;
  detail: string;
}

export interface HistoryResponse {
  entries: HistoryEntry[];
}

// ---------------------------------------------------------------------------
// Backend resource calls (served by the plugin's own Go backend).
// ---------------------------------------------------------------------------

export function getHealthz(): Promise<HealthzResponse> {
  return getBackendSrv().get<HealthzResponse>(`${RESOURCE_BASE_URL}/healthz`);
}

export function getConfig(): Promise<ConfigResponse> {
  return getBackendSrv().get<ConfigResponse>(`${RESOURCE_BASE_URL}/config`);
}

export function getActivities(): Promise<ActivitiesResponse> {
  return getBackendSrv().get<ActivitiesResponse>(`${RESOURCE_BASE_URL}/activities`);
}

export function getHistory(): Promise<HistoryResponse> {
  return getBackendSrv().get<HistoryResponse>(`${RESOURCE_BASE_URL}/history`);
}

export type TestKind = 'notification' | 'timeline';

export function sendTest(kind: TestKind): Promise<TestResponse> {
  return getBackendSrv().post<TestResponse>(`${RESOURCE_BASE_URL}/test`, { kind });
}

// ---------------------------------------------------------------------------
// Connect wizard provisioning — runs against Grafana's own API with the
// admin's session. Creates a service account + token, persists the token to
// plugin settings, and upserts a "PushWard" webhook contact point that loops
// alerts back into this plugin's /resources/webhook endpoint.
// ---------------------------------------------------------------------------

const WEBHOOK_SA_NAME = 'pushward-alerts-webhook';
const CONTACT_POINT_NAME = 'PushWard';

interface ServiceAccountDTO {
  id: number;
  name: string;
}

interface ServiceAccountSearchResult {
  serviceAccounts?: ServiceAccountDTO[];
}

interface TokenResponse {
  id: number;
  name: string;
  key: string;
}

interface ContactPoint {
  uid?: string;
  name: string;
  type: string;
  settings: Record<string, unknown>;
}

/** Absolute URL the contact point posts alerts to (loops back into this plugin). */
export function webhookUrl(): string {
  return `${window.location.origin}${RESOURCE_BASE_URL}/webhook`;
}

async function findServiceAccountId(name: string): Promise<number | undefined> {
  const res = await getBackendSrv().get<ServiceAccountSearchResult>('/api/serviceaccounts/search', {
    query: name,
  });
  return res.serviceAccounts?.find((sa) => sa.name === name)?.id;
}

async function ensureServiceAccount(): Promise<number> {
  const existing = await findServiceAccountId(WEBHOOK_SA_NAME);
  if (existing !== undefined) {
    return existing;
  }
  const created = await getBackendSrv().post<ServiceAccountDTO>(
    '/api/serviceaccounts',
    { name: WEBHOOK_SA_NAME, role: 'Viewer' },
    { showErrorAlert: false }
  );
  return created.id;
}

async function createServiceAccountToken(saId: number): Promise<string> {
  // Token names must be unique per service account; suffix so re-running Connect
  // (e.g. to rotate the secret) never collides with a previously-issued token.
  const tokenName = `${WEBHOOK_SA_NAME}-${Date.now()}`;
  const res = await getBackendSrv().post<TokenResponse>(
    `/api/serviceaccounts/${saId}/tokens`,
    { name: tokenName },
    { showErrorAlert: false }
  );
  return res.key;
}

async function saveWebhookToken(token: string, enabled: boolean, pinned: boolean): Promise<void> {
  // Send enabled/pinned so the settings POST doesn't disable the plugin (the
  // command's Enabled field is a non-pointer bool — omitting it means false).
  await lastValueFrom(
    getBackendSrv().fetch({
      url: `/api/plugins/${PLUGIN_ID}/settings`,
      method: 'POST',
      data: { enabled, pinned, secureJsonData: { webhookToken: token } },
      showErrorAlert: false,
    })
  );
}

async function upsertContactPoint(token: string): Promise<void> {
  const settings = {
    url: webhookUrl(),
    httpMethod: 'POST',
    authorization_scheme: 'Bearer',
    authorization_credentials: token,
  };
  // X-Disable-Provenance lets the contact point be edited in the UI later
  // (provisioned resources are otherwise read-only).
  const headers = { 'X-Disable-Provenance': 'true' };

  const existing = await getBackendSrv().get<ContactPoint[]>('/api/v1/provisioning/contact-points');
  const match = existing?.find((cp) => cp.name === CONTACT_POINT_NAME);

  if (match?.uid) {
    await lastValueFrom(
      getBackendSrv().fetch({
        url: `/api/v1/provisioning/contact-points/${match.uid}`,
        method: 'PUT',
        data: { uid: match.uid, name: CONTACT_POINT_NAME, type: 'webhook', settings },
        headers,
        showErrorAlert: false,
      })
    );
    return;
  }

  await lastValueFrom(
    getBackendSrv().fetch({
      url: '/api/v1/provisioning/contact-points',
      method: 'POST',
      data: { name: CONTACT_POINT_NAME, type: 'webhook', settings },
      headers,
      showErrorAlert: false,
    })
  );
}

/**
 * Runs the full Connect provisioning flow. Idempotent: reuses an existing
 * service account and updates an existing "PushWard" contact point in place.
 */
export async function connectToAlerting(enabled: boolean, pinned: boolean): Promise<void> {
  const saId = await ensureServiceAccount();
  const token = await createServiceAccountToken(saId);
  await saveWebhookToken(token, enabled, pinned);
  await upsertContactPoint(token);
}
