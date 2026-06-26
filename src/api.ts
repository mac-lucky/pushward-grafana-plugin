import { lastValueFrom } from 'rxjs';
import { config, getBackendSrv } from '@grafana/runtime';
import { PLUGIN_ID, RESOURCE_BASE_URL } from './constants';

// Normalize an error from a backend call into a human string. getBackendSrv
// rejects with a fetch-response-like object (not an Error), so unwrap its
// `data.message`/`statusText` before falling back to String(e) - otherwise the
// UI would render "[object Object]".
export function errorMessage(e: unknown): string {
  if (e instanceof Error) {
    return e.message;
  }
  if (e && typeof e === 'object') {
    const data = (e as { data?: { message?: string } }).data;
    if (data?.message) {
      return data.message;
    }
    const statusText = (e as { statusText?: string }).statusText;
    if (statusText) {
      return statusText;
    }
  }
  return String(e);
}

// ---------------------------------------------------------------------------
// Backend resource responses (see the Go backend's /resources routes).
// ---------------------------------------------------------------------------

export type ApiKeyStatus = 'valid' | 'rejected' | 'unknown';

export interface HealthzResponse {
  ok: boolean;
  apiKey: boolean;
  // Precise tri-state: 'unknown' is a transient 404/5xx/unreachable blip, not a
  // rejected key - render it as amber/grey, never as "invalid".
  apiKeyStatus: ApiKeyStatus;
  datasource: boolean;
  // Datasource selected AND a Grafana SA token is available (history actually works).
  history: boolean;
  // >=1 widget configured AND widget config parsed OK (engine publishing-ready).
  widgets: boolean;
  // Non-empty when the widget JSON failed to parse/validate.
  widgetsError: string;
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
  // Number of configured widgets; widgetsError is non-empty if their JSON is invalid.
  widgetCount: number;
  widgetsError: string;
}

export interface TestResponse {
  ok: boolean;
  message: string;
}

// Raw passthrough of PushWard's GET /activities, unwrapped by the backend into a
// plain array. Kept loose on purpose - the server adds fields over time.
export interface ActivitySummary {
  slug?: string;
  name?: string;
  state?: string;
  priority?: number;
  // The template lives under content.template on the server, not at the top level.
  content?: { template?: string; [key: string]: unknown };
  created_at?: string;
  updated_at?: string;
  [key: string]: unknown;
}

export interface ActivitiesResponse {
  activities: ActivitySummary[];
}

// Raw passthrough of PushWard's GET /widgets, unwrapped by the backend into a
// plain array. Each widget carries its rendered content under `content`.
export interface WidgetContent {
  template?: string;
  value?: unknown;
  unit?: string;
  [key: string]: unknown;
}

export interface WidgetSummary {
  slug?: string;
  name?: string;
  content?: WidgetContent;
  updated_at?: string;
  [key: string]: unknown;
}

export interface WidgetsResponse {
  widgets: WidgetSummary[];
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

export async function getActivities(): Promise<ActivitiesResponse> {
  const res = await getBackendSrv().get<ActivitiesResponse>(`${RESOURCE_BASE_URL}/activities`);
  // Backend always returns an array, but guard so the UI never maps over null.
  return { activities: res?.activities ?? [] };
}

export async function getWidgets(): Promise<WidgetsResponse> {
  const res = await getBackendSrv().get<WidgetsResponse>(`${RESOURCE_BASE_URL}/widgets`);
  return { widgets: res?.widgets ?? [] };
}

export function getHistory(): Promise<HistoryResponse> {
  return getBackendSrv().get<HistoryResponse>(`${RESOURCE_BASE_URL}/history`);
}

export type TestKind = 'notification' | 'timeline';

export function sendTest(kind: TestKind): Promise<TestResponse> {
  return getBackendSrv().post<TestResponse>(`${RESOURCE_BASE_URL}/test`, { kind });
}

// ---------------------------------------------------------------------------
// Connect wizard provisioning - runs against Grafana's own API with the
// admin's session. Creates a service account + token, persists the token to
// plugin settings, and upserts a "PushWard" webhook contact point that loops
// alerts back into this plugin's /resources/webhook endpoint.
// ---------------------------------------------------------------------------

const WEBHOOK_SA_NAME = 'pushward-alerts-webhook';
// Every webhook token is named with this prefix so old ones can be cleaned up.
const WEBHOOK_SA_TOKEN_PREFIX = `${WEBHOOK_SA_NAME}-`;
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

interface TokenDTO {
  id: number;
  name: string;
}

interface ContactPoint {
  uid?: string;
  name: string;
  type: string;
  settings: Record<string, unknown>;
}

/** Absolute URL the contact point posts alerts to (loops back into this plugin). */
export function webhookUrl(): string {
  // Build from Grafana's configured app URL (scheme + host + sub-path) instead of
  // window.location.origin, which drops the sub-path on reverse-proxy/sub-path
  // installs and would point the provisioned contact point at a 404.
  const base = config.appUrl.replace(/\/+$/, '');
  return `${base}${RESOURCE_BASE_URL}/webhook`;
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

async function createServiceAccountToken(saId: number): Promise<TokenResponse> {
  // Token names must be unique per service account; suffix so re-running Connect
  // (e.g. to rotate the secret) never collides with a previously-issued token.
  const tokenName = `${WEBHOOK_SA_TOKEN_PREFIX}${Date.now()}`;
  return getBackendSrv().post<TokenResponse>(
    `/api/serviceaccounts/${saId}/tokens`,
    { name: tokenName },
    { showErrorAlert: false }
  );
}

async function revokeStaleWebhookTokens(saId: number, keepTokenId: number): Promise<void> {
  try {
    const tokens = await getBackendSrv().get<TokenDTO[]>(
      `/api/serviceaccounts/${saId}/tokens`,
      undefined,
      undefined,
      { showErrorAlert: false }
    );
    const stale = (tokens ?? []).filter(
      (t) => t.id !== keepTokenId && typeof t.name === 'string' && t.name.startsWith(WEBHOOK_SA_TOKEN_PREFIX)
    );
    for (const t of stale) {
      try {
        await getBackendSrv().delete(`/api/serviceaccounts/${saId}/tokens/${t.id}`, undefined, {
          showErrorAlert: false,
        });
      } catch (e) {
        console.warn('PushWard: failed to revoke stale webhook token', t.id, e);
      }
    }
  } catch (e) {
    console.warn('PushWard: could not list service-account tokens for cleanup', e);
  }
}

async function saveWebhookToken(token: string, enabled: boolean, pinned: boolean): Promise<void> {
  // Grafana replaces jsonData wholesale on a settings POST (only omitted
  // secureJsonData keys are preserved). The Connect flow has no jsonData of its
  // own, so read the current jsonData and resend it unchanged; otherwise
  // connecting would wipe the datasource, widget config, and bridge settings the
  // user saved on the Configuration page. If the read fails we throw rather than
  // POST a blank jsonData, so a failure surfaces instead of silently wiping.
  const current = await getBackendSrv().get<{ jsonData?: Record<string, unknown> }>(
    `/api/plugins/${PLUGIN_ID}/settings`,
    undefined,
    undefined,
    { showErrorAlert: false }
  );
  const jsonData = current?.jsonData ?? {};

  // Send enabled/pinned so the settings POST doesn't disable the plugin (the
  // command's Enabled field is a non-pointer bool, so omitting it means false).
  await lastValueFrom(
    getBackendSrv().fetch({
      url: `/api/plugins/${PLUGIN_ID}/settings`,
      method: 'POST',
      data: { enabled, pinned, jsonData, secureJsonData: { webhookToken: token } },
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
  const created = await createServiceAccountToken(saId);
  await saveWebhookToken(created.key, enabled, pinned);
  await upsertContactPoint(created.key);
  // Revoke older webhook tokens only after the new one is persisted AND wired
  // into the contact point, so a valid token is always in place and a failure
  // mid-flow never leaves the contact point pointing at a revoked secret.
  // Best-effort: never touch the token just minted, never let cleanup abort the flow.
  await revokeStaleWebhookTokens(saId, created.id);
}
