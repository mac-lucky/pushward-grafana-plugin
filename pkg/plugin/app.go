package plugin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/resource/httpadapter"
	sdkconfig "github.com/grafana/grafana-plugin-sdk-go/config"
	"github.com/mac-lucky/pushward-integrations/shared/pushward"

	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/bridge"
	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/grafanaapi"
)

// Ensure App implements the SDK interfaces it relies on. CallResource is served
// through a custom method (so each request can refresh the Grafana connection)
// rather than the embedded httpadapter.
var (
	_ backend.CallResourceHandler   = (*App)(nil)
	_ instancemgmt.InstanceDisposer = (*App)(nil)
	_ backend.CheckHealthHandler    = (*App)(nil)
	_ backend.CollectMetricsHandler = (*App)(nil)
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
	metrics  *bridgeMetrics

	httpClient *http.Client // PushWard passthrough (/me, /activities)

	// Grafana connection (app URL + IAM service-account token), refreshed from
	// every request's GrafanaConfig. Read by the datasource-proxy querier and the
	// grafanaapi client; a stable token keeps background pollers authenticated
	// between requests.
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
		metrics:    newBridgeMetrics(),
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

	mux := http.NewServeMux()
	app.registerRoutes(mux)
	app.resource = httpadapter.New(mux)

	return app, nil
}

// CallResource refreshes the Grafana connection from the request context, then
// dispatches to the resource mux. The refresh keeps the IAM service-account
// token current for the bridge's background datasource-proxy queries.
func (a *App) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	a.refreshGrafanaConn(ctx)
	return a.resource.CallResource(ctx, req, sender)
}

// Dispose stops the bridge (sweeper/checker/pollers, draining in-flight work)
// and the Grafana API cache goroutine when Grafana recreates the instance.
func (a *App) Dispose() {
	if a.bridge != nil {
		a.bridge.Stop()
	}
	if a.grafana != nil {
		a.grafana.Close()
	}
}

// CheckHealth reports configuration readiness: the PushWard key must be present
// and accepted by api.pushward.app, and a datasource is required for timeline
// history (a missing datasource degrades to plain notifications, not an error).
func (a *App) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	a.refreshGrafanaConn(ctx)

	keyOK, detail := a.probeAPIKey(ctx)
	if !keyOK {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: detail}, nil
	}

	msg := "PushWard key valid."
	if a.settings.DatasourceUID == "" {
		msg = "PushWard key valid. Select a datasource on the Configuration page to enable timeline history."
	}
	return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: msg}, nil
}

// probeAPIKey verifies the PushWard key by calling GET {apiURL}/me. It returns
// whether the key is present AND accepted, plus a human-readable detail for the
// UI. A transport error or non-2xx (other than 401/403) is reported as a
// reachability problem rather than an outright-invalid key.
func (a *App) probeAPIKey(ctx context.Context) (ok bool, detail string) {
	if a.settings.APIKey == "" {
		return false, "PushWard API key not set"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet,
		strings.TrimRight(a.settings.APIURL, "/")+"/me", nil)
	if err != nil {
		return false, "could not build request: " + err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+a.settings.APIKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return false, "could not reach " + a.settings.APIURL
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, ""
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, "PushWard rejected the API key"
	default:
		return false, fmt.Sprintf("PushWard returned status %d", resp.StatusCode)
	}
}

// CollectMetrics exposes the bridge's Prometheus counters to Grafana.
func (a *App) CollectMetrics(_ context.Context, _ *backend.CollectMetricsRequest) (*backend.CollectMetricsResult, error) {
	payload, err := a.metrics.gather()
	if err != nil {
		return nil, err
	}
	return &backend.CollectMetricsResult{PrometheusMetrics: payload}, nil
}

// grafanaConn returns the current Grafana app URL and a token for Grafana's own
// API. It prefers the IAM external service-account token, but that requires the
// `externalServiceAccounts` feature toggle (off by default), so it falls back to
// the webhook service-account token minted by the Connect wizard — a Viewer SA
// that can query datasources through the proxy (verified). This keeps the
// timeline history query working on a stock Grafana with no feature toggles.
func (a *App) grafanaConn() (string, string) {
	a.connMu.RLock()
	url, tok := a.grafanaURL, a.grafanaTok
	a.connMu.RUnlock()
	if tok == "" {
		tok = a.settings.WebhookToken
	}
	return url, tok
}

// refreshGrafanaConn updates the stored Grafana app URL + IAM token from the
// request's GrafanaConfig. Empty/erroring values leave the prior value intact so
// a context without Grafana config (e.g. instance construction) doesn't clear a
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
// through Grafana's datasource proxy with the IAM service-account token. A fresh
// (stateless) metrics client is built per query so it always uses the current
// connection; the cost is negligible (a struct over a shared http.Client).
type dsQuerier struct {
	app *App
}

func (d *dsQuerier) client() (*bridge.MetricsClient, error) {
	url, tok := d.app.grafanaConn()
	if url == "" {
		return nil, errGrafanaURLUnavailable
	}
	if d.app.settings.DatasourceUID == "" {
		return nil, errNoDatasource
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
