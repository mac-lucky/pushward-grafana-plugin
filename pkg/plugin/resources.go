package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

// webhookResourcePath is the in-Grafana path the Connect wizard points the
// auto-created webhook contact point at (the frontend prepends the origin).
const webhookResourcePath = "/api/plugins/pushward-alerts-app/resources/webhook"

var (
	errGrafanaURLUnavailable = errors.New("grafana app URL unavailable (no request context yet)")
	errNoDatasource          = errors.New("no datasource configured for timeline history")
)

// registerRoutes wires the plugin's resource endpoints. Paths are relative to
// /api/plugins/pushward-alerts-app/resources.
func (a *App) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/config", a.handleConfig)
	mux.HandleFunc("/test", a.handleTest)
	mux.HandleFunc("/webhook", a.handleWebhook)
	mux.HandleFunc("/activities", a.handleActivities)
	mux.HandleFunc("/history", a.handleHistory)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleHealthz is a lightweight status probe for the UI.
func (a *App) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	apiKeyOK := a.settings.APIKey != ""
	dsOK := a.settings.DatasourceUID != ""
	msg := "ok"
	switch {
	case !apiKeyOK:
		msg = "PushWard API key not set"
	case !dsOK:
		msg = "No datasource selected (timeline history disabled)"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         apiKeyOK,
		"apiKey":     apiKeyOK,
		"datasource": dsOK,
		"message":    msg,
	})
}

// handleConfig echoes the non-secret configuration plus connection status. It
// never returns the API key or webhook token.
func (a *App) handleConfig(w http.ResponseWriter, _ *http.Request) {
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
		"apiKeySet":        s.APIKey != "",
		"webhookConnected": s.WebhookToken != "",
		"webhookUrl":       webhookResourcePath,
	})
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
func (a *App) handleActivities(w http.ResponseWriter, r *http.Request) {
	if a.settings.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "PushWard API key not set"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		strings.TrimRight(a.settings.APIURL, "/")+"/activities", nil)
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
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, resp.StatusCode, map[string]any{"error": strings.TrimSpace(string(body))})
		return
	}
	if len(body) == 0 || !json.Valid(body) {
		body = []byte("[]")
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"activities": json.RawMessage(body)})
}

// handleHistory returns the backend's recent delivery log (newest first).
func (a *App) handleHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"entries": a.delivery.Entries()})
}
