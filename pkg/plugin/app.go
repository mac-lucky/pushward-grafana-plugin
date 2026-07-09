package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/resource/httpadapter"
	sdkconfig "github.com/grafana/grafana-plugin-sdk-go/config"
	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	sharedwidgets "github.com/mac-lucky/pushward-integrations/shared/widgets"

	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/bridge"
	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/grafanaapi"
	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/widgets"
)

// Ensure App implements the SDK interfaces it relies on. CallResource is served
// through a custom method (so each request can refresh the Grafana connection)
// rather than the embedded httpadapter.
//
// backend.CollectMetricsHandler is deliberately absent: backend.Manage never
// wires one up (ServeOpts has no such field) and hands prometheus.DefaultGatherer
// straight to the diagnostics adapter, so the counters reach Grafana by being
// registered on the default registry. See metrics.go.
var (
	_ backend.CallResourceHandler   = (*App)(nil)
	_ instancemgmt.InstanceDisposer = (*App)(nil)
	_ backend.CheckHealthHandler    = (*App)(nil)
)

// App is the PushWard Grafana app-plugin backend. It owns the embedded bridge
// (Grafana alert webhook → PushWard timeline Live Activity) plus the management
// resource endpoints.
type App struct {
	resource backend.CallResourceHandler // httpadapter over the resource mux

	settings *Settings
	pw       *pushward.Client
	grafana  *grafanaapi.Client
	bridge   *bridge.Bridge
	delivery *DeliveryLog
	// metrics is the process-global counter singleton; every App instance shares
	// the same set (they must live on the one default registry Grafana scrapes).
	metrics *bridgeMetrics

	// Widget engine: the scheduled-PromQL publisher (shared/widgets.Manager).
	// widgetCancel cancels its poll context; widgetWG tracks the goroutine that
	// owns Start + Wait so Dispose can cancel and drain it deterministically.
	// widgetMu guards the publish-status fields, which the goroutine writes and
	// /healthz reads so the UI badge reflects whether widgets are *actually*
	// publishing rather than merely being configured.
	widgetCancel     context.CancelFunc
	widgetWG         sync.WaitGroup
	widgetMu         sync.Mutex
	widgetPublishing bool
	widgetStatusMsg  string

	httpClient *http.Client // PushWard passthrough (/auth/me, /activities, /widgets)

	// probe cache: dedupes the blocking GET /auth/me round-trip across the rapid
	// CheckHealth + /healthz calls a single Overview/config page load fires. The
	// result is reused for probeCacheTTL; the plugin instance is recreated on any
	// settings change, so the key can never change underneath a cached result.
	probeMu     sync.Mutex
	probeAt     time.Time
	probeStat   probeStatus
	probeDetail string
	probeCached bool

	// Grafana connection: app URL + the IAM external service-account token,
	// refreshed from every request's GrafanaConfig. grafanaTok is the IAM token
	// ONLY (empty unless the externalServiceAccounts toggle is on). The
	// provisioning/alertmanager client reads it via grafanaConn(); the
	// datasource-proxy querier instead prefers the webhook SA token via
	// grafanaConnDatasource(). A stable token keeps background pollers
	// authenticated between requests.
	connMu     sync.RWMutex
	grafanaURL string
	grafanaTok string
}

// NewApp creates and wires a new App instance.
func NewApp(ctx context.Context, settings backend.AppInstanceSettings) (instancemgmt.Instance, error) {
	s, err := LoadSettings(settings)
	if err != nil {
		return nil, err
	}

	app := &App{
		settings:   s,
		delivery:   NewDeliveryLog(),
		metrics:    sharedBridgeMetrics(),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}

	app.pw = pushward.NewClient(s.APIURL, s.APIKey)
	app.grafana = grafanaapi.NewClient(app.grafanaConn)

	querier := &dsQuerier{app: app}
	poller := bridge.NewPoller(querier, app.pw, s.PollInterval)
	app.bridge = bridge.NewBridge(app.pw, querier, app.grafana, poller, app.delivery, app.metrics, bridge.Config{
		HistoryWindow:   s.HistoryWindow,
		Priority:        s.Priority,
		CleanupDelay:    s.CleanupDelay,
		StaleTimeout:    s.StaleTimeout,
		SeverityLabel:   s.SeverityLabel,
		DefaultSeverity: s.DefaultSeverity,
		Smoothing:       &s.Smoothing,
		Scale:           s.Scale,
		Decimals:        &s.Decimals,
	})

	// Seed the Grafana connection from the construction context if present; it is
	// refreshed on every CallResource/CheckHealth regardless.
	app.refreshGrafanaConn(ctx)

	app.startWidgetEngine(querier)

	mux := http.NewServeMux()
	app.registerRoutes(mux)
	app.resource = httpadapter.New(mux)

	return app, nil
}

// setWidgetStatus records whether the widget engine is actively publishing and a
// human-readable reason when it is not, so /healthz reports the true state rather
// than mere config presence.
func (a *App) setWidgetStatus(publishing bool, msg string) {
	a.widgetMu.Lock()
	a.widgetPublishing = publishing
	a.widgetStatusMsg = msg
	a.widgetMu.Unlock()
}

// widgetStatus reports whether widgets are publishing plus a status/error
// message. An empty message with publishing=false means simply "no widgets
// configured" (idle, not an error); a non-empty message means configured widgets
// cannot publish yet and explains why.
func (a *App) widgetStatus() (publishing bool, msg string) {
	if a.settings.WidgetsError != "" {
		return false, "widget config invalid: " + a.settings.WidgetsError
	}
	if len(a.settings.Widgets) == 0 {
		return false, ""
	}
	a.widgetMu.Lock()
	defer a.widgetMu.Unlock()
	return a.widgetPublishing, a.widgetStatusMsg
}

// startWidgetEngine builds the scheduled-PromQL widget manager from the parsed
// widget config and runs it in a single owned goroutine (Start then Wait), so
// Dispose can cancel its context and drain it without racing the initial polls.
// It never fails plugin construction (the timeline path stays up) and records a
// publish status the UI can surface honestly.
//
// The engine is gated on a datasource AND a datasource-proxy token being present:
// widgets poll through the proxy, so starting before the Connect wizard has run
// would publish empty placeholder widgets to the user's device and log a failure
// every interval. When the gate isn't met the engine stays off with an
// explanatory status; Grafana recreates the instance on the next settings save
// (which is exactly when a datasource pick or Connect run lands), re-running this
// with the gate satisfied.
func (a *App) startWidgetEngine(querier *dsQuerier) {
	if a.settings.WidgetsError != "" {
		slog.Error("widget config invalid; widget engine disabled", "error", a.settings.WidgetsError)
		return // widgetStatus() reports the parse error directly from settings
	}
	if len(a.settings.Widgets) == 0 {
		return
	}
	if a.settings.APIKey == "" {
		// Publishing needs the hlk_ key; without it every poll would query the
		// datasource and then fail the CreateWidget/PATCH against PushWard. Stay
		// off until the key is set (saving it recreates this instance).
		a.setWidgetStatus(false, "set the PushWard API key to publish widgets")
		return
	}
	if a.settings.DatasourceUID == "" {
		a.setWidgetStatus(false, "select a datasource to start publishing widgets")
		return
	}
	if !a.historyTokenAvailable() {
		a.setWidgetStatus(false, "run the Connect wizard to authorize datasource queries for widgets")
		return
	}
	specs, err := widgets.BuildSpecs(a.settings.Widgets, querier)
	if err != nil {
		a.setWidgetStatus(false, "building widget specs failed: "+err.Error())
		slog.Error("building widget specs failed; widget engine disabled", "error", err)
		return
	}
	mgr, err := sharedwidgets.New(a.pw, specs, slog.Default())
	if err != nil {
		a.setWidgetStatus(false, "creating widget manager failed: "+err.Error())
		slog.Error("creating widget manager failed; widget engine disabled", "error", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.widgetCancel = cancel
	a.setWidgetStatus(false, "widget engine starting")
	a.widgetWG.Add(1)
	go func() {
		defer a.widgetWG.Done()
		// Claim "publishing" only once Start succeeds. Start runs each widget's
		// first poll + CreateWidget synchronously, so a rejected key (e.g. one
		// missing the "widgets" scope, which 403s the create) surfaces here and
		// the status reflects it instead of staying optimistically green. The one
		// residual gap: a gauge/progress widget with no data at startup defers its
		// create out of Start, so a later persistent failure on that lone widget
		// is logged rather than reflected in the badge.
		if err := mgr.Start(ctx); err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Error("widget manager start failed", "error", err)
				a.setWidgetStatus(false, "widget publishing failed: "+err.Error())
			}
		} else {
			a.setWidgetStatus(true, "")
		}
		// Block until every poll goroutine exits (after cancel), so widgetWG.Wait
		// in Dispose is a true drain.
		mgr.Wait()
	}()
	slog.Info("widget engine started", "widgets", len(a.settings.Widgets))
}

// CallResource refreshes the Grafana connection from the request context, then
// dispatches to the resource mux. The refresh keeps the IAM service-account
// token current for the bridge's background datasource-proxy queries.
func (a *App) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	a.refreshGrafanaConn(ctx)
	return a.resource.CallResource(ctx, req, sender)
}

// Dispose stops the bridge (sweeper/checker/pollers, draining in-flight work),
// the widget engine (poll goroutines), and the Grafana API cache goroutine when
// Grafana recreates the instance.
func (a *App) Dispose() {
	if a.widgetCancel != nil {
		a.widgetCancel()
	}
	a.widgetWG.Wait()
	if a.bridge != nil {
		a.bridge.Stop()
	}
	if a.grafana != nil {
		a.grafana.Close()
	}
}

// probeStatus is the tri-state result of validating the PushWard key. It keeps
// a "reachable but ambiguous" outcome (a 404/5xx/transport blip) distinct from
// an outright-rejected key so a transient hiccup never reds the key as invalid.
type probeStatus int

const (
	probeValid    probeStatus = iota // 2xx: key present and accepted
	probeRejected                    // 401/403: key present but rejected
	probeUnknown                     // reachable but ambiguous (404/5xx/transport blip)
	probeUnset                       // no key configured at all (a definite misconfig)
)

// CheckHealth reports configuration readiness: the PushWard key must be present
// and accepted by api.pushward.app, and a datasource is required for timeline
// history (a missing datasource degrades to plain notifications, not an error).
func (a *App) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	a.refreshGrafanaConn(ctx)

	switch status, detail := a.probeAPIKeyCached(ctx); status {
	case probeRejected, probeUnset:
		// A rejected key or no key at all is a definite misconfiguration: report
		// Error (red) so a keyless/misconfigured plugin trips a health alarm.
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: detail}, nil
	case probeUnknown:
		// Reachable-but-ambiguous (404/5xx/transport blip): not a confirmed-invalid
		// key, so report Unknown rather than Error to avoid a false "key rejected".
		return &backend.CheckHealthResult{Status: backend.HealthStatusUnknown, Message: detail}, nil
	}

	msg := "PushWard key valid."
	switch {
	case a.settings.DatasourceUID == "":
		msg = "PushWard key valid. Select a datasource on the Configuration page to enable timeline history."
	case !a.historyTokenAvailable():
		msg = "PushWard key valid. Run the Connect wizard to enable timeline history — datasource queries need a Grafana service-account token."
	}
	if _, wmsg := a.widgetStatus(); wmsg != "" {
		msg += " Widgets: " + wmsg + "."
	}
	return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: msg}, nil
}

// probeAPIKey verifies the PushWard key by calling GET {apiURL}/auth/me, the
// profile endpoint an hlk_ integration key (activity:update scope) is authorized
// for. It returns a tri-state status plus a human-readable detail for the UI: a
// 2xx is valid, a 401/403 is a rejected key, and anything else (a 404/5xx, an
// unexpected status, a transport error, or an absent key) is "unknown": never
// reported as outright-invalid, so a gateway blip can't dark the health surface.
func (a *App) probeAPIKey(ctx context.Context) (status probeStatus, detail string) {
	if a.settings.APIKey == "" {
		return probeUnset, "PushWard API key not set"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet,
		strings.TrimRight(a.settings.APIURL, "/")+"/auth/me", nil)
	if err != nil {
		return probeUnknown, "could not build request: " + err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+a.settings.APIKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return probeUnknown, "could not reach " + a.settings.APIURL
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return probeValid, ""
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return probeRejected, "PushWard rejected the API key"
	default:
		return probeUnknown, fmt.Sprintf("PushWard reachable but returned status %d", resp.StatusCode)
	}
}

// probeCacheTTL bounds how long a probeAPIKey result is reused. Short enough
// that fixing a key reflects on the next refresh, long enough to collapse the
// back-to-back CheckHealth + /healthz calls a single page load fires.
const probeCacheTTL = 10 * time.Second

// probeAPIKeyCached returns a recent probeAPIKey result when one is still fresh,
// so opening the Overview and Configuration pages back-to-back does not issue a
// separate /auth/me round-trip per call for the same unchanged key.
func (a *App) probeAPIKeyCached(ctx context.Context) (probeStatus, string) {
	a.probeMu.Lock()
	if a.probeCached && time.Since(a.probeAt) < probeCacheTTL {
		st, det := a.probeStat, a.probeDetail
		a.probeMu.Unlock()
		return st, det
	}
	a.probeMu.Unlock()

	st, det := a.probeAPIKey(ctx)

	a.probeMu.Lock()
	a.probeStat, a.probeDetail, a.probeAt, a.probeCached = st, det, time.Now(), true
	a.probeMu.Unlock()
	return st, det
}

// grafanaConn returns the Grafana app URL and the IAM external service-account
// token, used by the grafanaapi client for Grafana's own provisioning + alertmanager
// APIs (rule-query extract, alert-state backstop). Those calls need plugin-scoped
// permissions, so there is deliberately NO webhook-token fallback here: the webhook
// SA is a Viewer that can be folder-RBAC-scoped, and feeding it to the alertmanager
// backstop would let a scoped-empty alert list look like "resolved" and prematurely
// end a still-firing activity. When the IAM token is absent (the
// externalServiceAccounts toggle is off — the default), the token is empty and these
// calls fail closed: the backstop stays an inert no-op and the staleTimeout sweeper
// handles cleanup, exactly as documented.
func (a *App) grafanaConn() (string, string) {
	a.connMu.RLock()
	defer a.connMu.RUnlock()
	return a.grafanaURL, a.grafanaTok
}

// grafanaConnDatasource returns the Grafana app URL and the token for querying the
// datasource proxy (timeline history). It prefers the webhook service-account token
// minted by the Connect wizard — a Viewer SA that can query datasources through the
// proxy (verified live) and is stable across the instance lifetime — and falls back
// to the IAM token when the wizard hasn't run. A Viewer SA is sufficient here (unlike
// the provisioning/alertmanager path), so the fallback is safe; preferring the stable
// webhook token also means a rotated or revoked IAM token can't silently dark history
// queries. Returns an empty token when neither is available; callers must guard.
func (a *App) grafanaConnDatasource() (string, string) {
	a.connMu.RLock()
	url, iamTok := a.grafanaURL, a.grafanaTok
	a.connMu.RUnlock()
	if tok := a.settings.WebhookToken; tok != "" {
		return url, tok
	}
	return url, iamTok
}

// historyTokenAvailable reports whether any token usable for the datasource-proxy
// history query is present (webhook SA token or IAM token). When false but a
// datasource is selected, timeline history is disabled and the user must run the
// Connect wizard (or enable the externalServiceAccounts toggle).
func (a *App) historyTokenAvailable() bool {
	if a.settings.WebhookToken != "" {
		return true
	}
	a.connMu.RLock()
	defer a.connMu.RUnlock()
	return a.grafanaTok != ""
}

// refreshGrafanaConn updates the stored Grafana app URL + IAM token from the
// request's GrafanaConfig. grafanaTok holds the IAM token only; an empty IAM
// token is normal (the externalServiceAccounts toggle is off by default), in
// which case the datasource-proxy path uses the webhook token instead (see
// grafanaConnDatasource). Empty/erroring values leave the prior value intact so a
// context without Grafana config (e.g. instance construction) doesn't clear a
// good connection.
func (a *App) refreshGrafanaConn(ctx context.Context) {
	cfg := sdkconfig.GrafanaConfigFromContext(ctx)
	if cfg == nil {
		return
	}
	url, urlErr := cfg.AppURL()
	tok, tokErr := cfg.PluginAppClientSecret()

	a.connMu.Lock()
	if urlErr == nil && url != "" {
		a.grafanaURL = strings.TrimRight(url, "/")
	}
	if tokErr == nil && tok != "" {
		a.grafanaTok = tok
	}
	a.connMu.Unlock()
}

// dsQuerier implements bridge.MetricsQuerier by querying the configured datasource
// through Grafana's datasource proxy. It authenticates with grafanaConnDatasource()
// — the webhook SA token preferred, IAM token as fallback — NOT the IAM-only
// grafanaConn() the provisioning/alertmanager client uses; the two accessors are
// intentionally distinct (see grafanaConnDatasource). A fresh (stateless) metrics
// client is built per query so it always uses the current connection; the cost is
// negligible (a struct over a shared http.Client).
type dsQuerier struct {
	app *App
}

func (d *dsQuerier) client() (*bridge.MetricsClient, error) {
	url, tok := d.app.grafanaConnDatasource()
	if url == "" {
		return nil, errGrafanaURLUnavailable
	}
	if d.app.settings.DatasourceUID == "" {
		return nil, errNoDatasource
	}
	// Guard an empty (or whitespace-only) token explicitly: without it the proxy is
	// hit with a bare "Authorization: Bearer " header and 401s, silently darkening
	// history with a confusing error. A clear message points the user at the Connect
	// wizard. TrimSpace so a blank token also fails closed rather than sending "Bearer  ".
	if strings.TrimSpace(tok) == "" {
		return nil, errNoGrafanaToken
	}
	base := grafanaapi.DatasourceProxyURL(url, d.app.settings.DatasourceUID)
	return bridge.NewMetricsClient(base, bridge.WithBearerToken(tok)), nil
}

func (d *dsQuerier) QueryRangeAll(ctx context.Context, expr string, from, to time.Time, step time.Duration) ([]bridge.LabeledSeries, error) {
	c, err := d.client()
	if err != nil {
		return nil, err
	}
	return c.QueryRangeAll(ctx, expr, from, to, step)
}

func (d *dsQuerier) QueryInstantAll(ctx context.Context, expr string, ts time.Time) ([]bridge.LabeledPoint, error) {
	c, err := d.client()
	if err != nil {
		return nil, err
	}
	return c.QueryInstantAll(ctx, expr, ts)
}
