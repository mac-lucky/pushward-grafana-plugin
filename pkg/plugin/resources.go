package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"

	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/bridge"
)

// webhookResourcePath is the in-Grafana path the Connect wizard points the
// auto-created webhook contact point at (the frontend prepends the origin).
const webhookResourcePath = "/api/plugins/pushward-alerts-app/resources/webhook"

var (
	errGrafanaURLUnavailable = errors.New("grafana app URL unavailable (no request context yet)")
	errNoDatasource          = errors.New("no datasource configured for timeline history")
	errNoGrafanaToken        = errors.New("no Grafana service-account token for datasource history: run the Connect wizard (or enable the externalServiceAccounts feature toggle)")
)

// registerRoutes wires the plugin's resource endpoints. Paths are relative to
// /api/plugins/pushward-alerts-app/resources.
func (a *App) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/config", a.handleConfig)
	mux.HandleFunc("/test", a.handleTest)
	mux.HandleFunc("/webhook", a.handleWebhook)
	mux.HandleFunc("/activities", a.handleActivities)
	mux.HandleFunc("/activities/end", a.handleEndActivity)
	mux.HandleFunc("/active", a.handleActive)
	mux.HandleFunc("/widgets", a.handleWidgets)
	mux.HandleFunc("/history", a.handleHistory)
	mux.HandleFunc("/stats", a.handleStats)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// requireGet rejects any verb other than GET on a read endpoint, mirroring the
// explicit POST guards on handleTest/handleWebhook. Returns true when the
// request may proceed.
func requireGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return false
	}
	return true
}

// listEnvelope is the server's list response shape. NextCursor is empty on the
// final (or only) page; /widgets is unpaginated and never sets it.
type listEnvelope struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

// handleHealthz is the status probe for the UI. It validates the key against
// api.pushward.app (GET /auth/me), mirroring CheckHealth so the badges and the
// plugin health page agree. apiKey is the confirmed-valid flag; apiKeyStatus is
// the precise tri-state ("valid"/"rejected"/"unknown") so the UI can tell a
// rejected key from a transient reachability blip.
func (a *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	status, detail := a.probeAPIKeyCached(r.Context())
	keyValid := status == probeValid
	dsOK := a.settings.DatasourceUID != ""
	historyOK := dsOK && a.historyTokenAvailable()
	widgetsPublishing, widgetsMsg := a.widgetStatus()

	statusStr := "unknown"
	switch status {
	case probeValid:
		statusStr = "valid"
	case probeRejected:
		statusStr = "rejected"
	}

	msg := "ok"
	switch {
	case status != probeValid:
		msg = detail
	case !dsOK:
		msg = "No datasource selected (timeline history disabled)"
	case !historyOK:
		msg = "Datasource selected, but timeline history is disabled — run the Connect wizard to authorize datasource queries"
	}
	// Benign widget setup states (no datasource/key yet, engine starting) ride in
	// the message text; only a genuine parse/validate failure populates the
	// dedicated widgetsError field that the UI renders as a configuration error.
	if a.settings.WidgetsError == "" && widgetsMsg != "" {
		msg += " | Widgets: " + widgetsMsg
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           keyValid,
		"apiKey":       keyValid,
		"apiKeyStatus": statusStr,
		"datasource":   dsOK,
		"history":      historyOK,
		"widgets":      widgetsPublishing,
		"widgetsError": a.settings.WidgetsError,
		"message":      msg,
	})
}

// handleConfig echoes the non-secret configuration plus connection status. It
// never returns the API key or webhook token.
func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	s := a.settings
	writeJSON(w, http.StatusOK, map[string]any{
		"apiUrl":           s.APIURL,
		"datasourceUid":    s.DatasourceUID,
		"severityLabel":    s.SeverityLabel,
		"defaultSeverity":  s.DefaultSeverity,
		"priority":         s.Priority,
		"historyWindow":    s.HistoryWindow.String(),
		"pollInterval":     s.PollInterval.String(),
		"cleanupDelay":     s.CleanupDelay.String(),
		"staleTimeout":     s.StaleTimeout.String(),
		"smoothing":        s.Smoothing,
		"scale":            s.Scale,
		"decimals":         s.Decimals,
		"widgetCount":      len(s.Widgets),
		"widgetsError":     s.WidgetsError,
		"apiKeySet":        s.APIKey != "",
		"webhookConnected": s.WebhookToken != "",
		"webhookUrl":       webhookResourcePath,
	})
}

// endActivityRequest is the body for POST /activities/end.
type endActivityRequest struct {
	Slug string `json:"slug"`
}

// handleEndActivity ends a running Live Activity on the user's behalf. It first
// tells the bridge to forget the slug (so the poller / alertmanager backstop
// won't resurrect an alert-driven activity), then sends a terminal ENDED state
// to PushWard. Ending via state=ended uses the hlk_ key's activity:update scope
// already required by the bridge - no extra scope needed.
func (a *App) handleEndActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "message": "method not allowed"})
		return
	}
	if a.settings.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "PushWard API key not set"})
		return
	}
	var body endActivityRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || strings.TrimSpace(body.Slug) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "slug required"})
		return
	}

	if a.bridge != nil {
		a.bridge.Forget(body.Slug)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := a.pw.PatchActivity(ctx, body.Slug, pushward.PatchRequest{State: pushward.StateEnded}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleActive returns the bridge's currently tracked alerts so the Activities
// page can map an activity slug to the alert identity (rule UID / alertname) it
// needs to build silence matchers.
func (a *App) handleActive(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	var active []bridge.ActiveAlert
	if a.bridge != nil {
		active = a.bridge.ActiveAlerts()
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active})
}

type testRequest struct {
	Kind string `json:"kind"`
}

// handleTest sends a real test notification or timeline to PushWard so the user
// can verify their key + device end to end from the Connect page.
func (a *App) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "message": "method not allowed"})
		return
	}
	if a.settings.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "PushWard API key not set"})
		return
	}

	var body testRequest
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body) // default to notification on error

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var (
		err error
		msg string
	)
	switch body.Kind {
	case "timeline":
		err = a.sendTestTimeline(ctx)
		msg = "Test timeline Live Activity sent — check your iPhone."
	default:
		err = a.pw.SendNotification(ctx, pushward.SendNotificationRequest{
			Title:    "PushWard test",
			Subtitle: "Grafana",
			Body:     "Your Grafana → PushWard connection works.",
			ThreadID: "grafana",
			Source:   "grafana",
			Level:    pushward.LevelActive,
			Push:     true,
		})
		msg = "Test notification sent — check your iPhone."
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

// sendTestTimeline creates a short-lived timeline activity seeded with a
// synthetic rising series so the user sees the real sparkline rendering.
func (a *App) sendTestTimeline(ctx context.Context) error {
	const slug = "grafana-plugin-test"
	if err := a.pw.CreateActivity(ctx, slug, "PushWard Test Alert", a.settings.Priority,
		int(a.settings.CleanupDelay.Seconds()), 120); err != nil {
		return err
	}

	now := time.Now().Unix()
	const points = 12
	series := make([]pushward.HistoryPoint, 0, points)
	for i := points - 1; i >= 0; i-- {
		// A gentle sine-ish ramp from ~45 to ~92 so the sparkline has shape.
		frac := float64(points-1-i) / float64(points-1)
		v := 45 + 47*frac + 4*math.Sin(frac*math.Pi*2)
		series = append(series, pushward.HistoryPoint{
			Timestamp: now - int64(i*60),
			Value:     math.Round(v*10) / 10,
		})
	}

	content := pushward.Content{
		Template:    pushward.TemplateTimeline,
		Value:       map[string]float64{"test": series[len(series)-1].Value},
		History:     map[string][]pushward.HistoryPoint{"test": series},
		Subtitle:    "Grafana",
		AccentColor: pushward.SeverityColor(pushward.SeverityCritical),
		Icon:        pushward.SeverityIcon(pushward.SeverityCritical, "exclamationmark.triangle.fill"),
		Unit:        "%",
		State:       "Test alert firing",
		Smoothing:   &a.settings.Smoothing,
		Scale:       a.settings.Scale,
		Decimals:    &a.settings.Decimals,
	}
	return a.pw.UpdateActivity(ctx, slug, pushward.UpdateRequest{
		State:   pushward.StateOngoing,
		Content: content,
	})
}

// handleWebhook is the embedded-bridge entry point (the self-loop target). It
// answers 200 immediately so Grafana's alerting engine never retries, and hands
// the payload to the bridge for async processing.
func (a *App) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"status": "method not allowed"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "bad request"})
		return
	}
	if perr := a.bridge.ProcessWebhook(r.Context(), body); perr != nil {
		// Malformed payload: log and still 200 so the alerting engine doesn't
		// retry an un-parseable body forever.
		slog.Warn("invalid grafana webhook payload", "error", perr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleActivities proxies the user's current Live Activities from PushWard.
// GET /activities is cursor-paginated via the opaque `after` token.
func (a *App) handleActivities(w http.ResponseWriter, r *http.Request) {
	a.proxyList(w, r, "/activities", "activities", "after")
}

// handleWidgets proxies the user's current PushWard widgets, so the Widgets
// management page can list what the engine is publishing. GET /widgets is
// unpaginated (no cursor), so it is fetched in a single request.
func (a *App) handleWidgets(w http.ResponseWriter, r *http.Request) {
	a.proxyList(w, r, "/widgets", "widgets", "")
}

// proxyList GETs a PushWard list endpoint and returns its items as a bare array
// under responseKey, so the frontend always receives `{responseKey: [...]}`.
// When cursorParam is non-empty the endpoint is cursor-paginated (/activities
// uses "after"): every page is followed and concatenated so the management table
// is never silently truncated to the first server page. Read-only; GET-only.
func (a *App) proxyList(w http.ResponseWriter, r *http.Request, upstreamPath, responseKey, cursorParam string) {
	if !requireGet(w, r) {
		return
	}
	if a.settings.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "PushWard API key not set"})
		return
	}

	base := strings.TrimRight(a.settings.APIURL, "/") + upstreamPath
	items := []json.RawMessage{}
	cursor := ""
	// Safety bound: at the 100-item server page cap this covers 5,000 items, far
	// past any real per-user activity/widget count, while preventing an unbounded
	// loop if the server ever returned a self-referential cursor.
	const maxPages = 50
	for page := 0; page < maxPages; page++ {
		u := base
		if cursorParam != "" {
			q := url.Values{"limit": {"100"}}
			if cursor != "" {
				q.Set(cursorParam, cursor)
			}
			u += "?" + q.Encode()
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		req.Header.Set("Authorization", "Bearer "+a.settings.APIKey)

		resp, err := a.httpClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeJSON(w, resp.StatusCode, map[string]any{"error": strings.TrimSpace(string(body))})
			return
		}

		var env listEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "unexpected list response from PushWard"})
			return
		}
		for _, it := range env.Items {
			if t := bytes.TrimSpace(it); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
				items = append(items, it)
			}
		}
		// Stop on the last page, on an unpaginated endpoint, or if the server
		// echoed the same cursor (defensive: would otherwise loop).
		if cursorParam == "" || env.NextCursor == "" || env.NextCursor == cursor {
			break
		}
		cursor = env.NextCursor
		if page == maxPages-1 {
			slog.Warn("proxyList page cap reached; list may be truncated", "path", upstreamPath, "pages", maxPages)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{responseKey: items})
}

// handleHistory returns the backend's recent delivery log (newest first).
func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": a.delivery.Entries()})
}

// handleStats returns the bridge delivery counters as JSON. It reads the same
// collectors Grafana exports at /metrics/plugins/pushward-alerts-app, so the
// Overview page shows real numbers without anyone configuring a Prometheus
// scrape. The counters reset when the plugin process restarts.
func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, a.metrics.Snapshot())
}
