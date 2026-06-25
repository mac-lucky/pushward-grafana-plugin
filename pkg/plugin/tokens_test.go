package plugin

import (
	"errors"
	"testing"
)

// TestGrafanaConnIsIAMOnly verifies the
// provisioning/alertmanager accessor must NEVER yield the webhook (Viewer) token,
// because feeding a folder-scoped Viewer to the alert-state backstop can falsely
// resolve a still-firing activity.
func TestGrafanaConnIsIAMOnly(t *testing.T) {
	app := &App{settings: &Settings{WebhookToken: "wh-token"}}
	app.grafanaURL = "https://g.example"
	app.grafanaTok = "iam-token"

	if url, tok := app.grafanaConn(); url != "https://g.example" || tok != "iam-token" {
		t.Fatalf("grafanaConn = (%q,%q), want (https://g.example, iam-token)", url, tok)
	}

	// With no IAM token it must NOT fall back to the webhook token.
	app.grafanaTok = ""
	if _, tok := app.grafanaConn(); tok != "" {
		t.Errorf("grafanaConn leaked the webhook token to the IAM-only path: %q", tok)
	}
}

// TestGrafanaConnDatasourcePrefersWebhook covers the datasource-proxy accessor:
// prefer the stable webhook token, fall back to IAM, else empty.
func TestGrafanaConnDatasourcePrefersWebhook(t *testing.T) {
	app := &App{settings: &Settings{WebhookToken: "wh-token"}}
	app.grafanaURL = "https://g.example"
	app.grafanaTok = "iam-token"

	if _, tok := app.grafanaConnDatasource(); tok != "wh-token" {
		t.Errorf("datasource token = %q, want webhook token preferred", tok)
	}

	app.settings.WebhookToken = ""
	if _, tok := app.grafanaConnDatasource(); tok != "iam-token" {
		t.Errorf("datasource token = %q, want IAM fallback", tok)
	}

	app.grafanaTok = ""
	if _, tok := app.grafanaConnDatasource(); tok != "" {
		t.Errorf("datasource token = %q, want empty when neither present", tok)
	}
}

// TestDatasourceClientFailsClosedWithoutToken verifies that a missing/blank token must surface errNoGrafanaToken, never build a client
// that hits the proxy with a bare "Authorization: Bearer " header.
func TestDatasourceClientFailsClosedWithoutToken(t *testing.T) {
	app := &App{settings: &Settings{DatasourceUID: "ds-uid"}}
	app.grafanaURL = "https://g.example"
	d := &dsQuerier{app: app}

	if _, err := d.client(); !errors.Is(err, errNoGrafanaToken) {
		t.Fatalf("client() with no token err = %v, want errNoGrafanaToken", err)
	}

	// Whitespace-only token must also fail closed.
	app.settings.WebhookToken = "   "
	if _, err := d.client(); !errors.Is(err, errNoGrafanaToken) {
		t.Fatalf("client() with blank token err = %v, want errNoGrafanaToken", err)
	}

	// A real token builds a client.
	app.settings.WebhookToken = "wh-token"
	if c, err := d.client(); err != nil || c == nil {
		t.Fatalf("client() with token = (%v, %v), want a non-nil client", c, err)
	}
}

func TestHistoryTokenAvailable(t *testing.T) {
	app := &App{settings: &Settings{}}
	if app.historyTokenAvailable() {
		t.Error("historyTokenAvailable = true with no tokens")
	}

	app.settings.WebhookToken = "wh"
	if !app.historyTokenAvailable() {
		t.Error("historyTokenAvailable = false with a webhook token present")
	}

	app.settings.WebhookToken = ""
	app.grafanaTok = "iam"
	if !app.historyTokenAvailable() {
		t.Error("historyTokenAvailable = false with an IAM token present")
	}
}
