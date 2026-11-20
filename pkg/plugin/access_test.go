package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// Who may call each resource route: the webhook only the Connect wizard's
// service account, the test push and End only an org Admin, the rest an
// Editor or Admin. Nobody without a login (anonymous access) gets anywhere.
func TestResourceAccess(t *testing.T) {
	var (
		anonymous = &backend.User{Role: "Viewer"} // Grafana's anonymous user has no login
		viewer    = &backend.User{Login: "viewer", Role: "Viewer"}
		editor    = &backend.User{Login: "editor", Role: "Editor"}
		admin     = testAdmin
		webhookSA = &backend.User{Login: "sa-1-pushward-alerts-webhook", Name: "pushward-alerts-webhook", Role: "Viewer"}
		// The login service accounts had before Grafana 11 renamed them. No
		// service account has it on Grafana 11+, so a user could take it.
		bareSA = &backend.User{Login: "sa-pushward-alerts-webhook", Name: "pushward-alerts-webhook", Role: "Viewer"}
		// The same account name in another org.
		otherOrgSA = &backend.User{Login: "sa-2-pushward-alerts-webhook", Name: "pushward-alerts-webhook", Role: "Viewer"}
		// A user whose display name copies the service account's.
		lookalike = &backend.User{Login: "mallory", Name: "pushward-alerts-webhook", Role: "Viewer"}
	)
	callers := []struct {
		name string
		user *backend.User
	}{
		{"no user", nil}, {"anonymous", anonymous}, {"viewer", viewer}, {"editor", editor}, {"admin", admin},
		{"webhook service account", webhookSA}, {"bare sa- login", bareSA},
		{"service account of another org", otherOrgSA}, {"look-alike name", lookalike},
	}
	routes := []struct {
		method, path string
		body         string
		allowed      map[string]bool
	}{
		{http.MethodPost, "webhook", `{"alerts":[]}`, map[string]bool{"webhook service account": true}},
		{http.MethodPost, "test", `{"kind":"notification"}`, map[string]bool{"admin": true}},
		{http.MethodPost, "activities/end", `{}`, map[string]bool{"admin": true}},
	}
	for _, path := range []string{"healthz", "config", "activities", "active", "widgets", "history", "stats"} {
		routes = append(routes, struct {
			method, path string
			body         string
			allowed      map[string]bool
		}{http.MethodGet, path, "", map[string]bool{"editor": true, "admin": true}})
	}

	app, _, _ := newTestAppWith(t, map[string]string{"webhookToken": "glsa_test"}, nil)
	for _, rt := range routes {
		for _, c := range callers {
			t.Run(fmt.Sprintf("%s %s as %s", rt.method, rt.path, c.name), func(t *testing.T) {
				var body []byte
				if rt.body != "" {
					body = []byte(rt.body)
				}
				resp := callResourceAs(t, context.Background(), app, c.user, rt.method, rt.path, body)
				if denied := resp.Status == http.StatusForbidden; denied == rt.allowed[c.name] {
					t.Errorf("status %d (%s), want allowed=%v", resp.Status, resp.Body, rt.allowed[c.name])
				}
				if resp.Status != http.StatusForbidden {
					return
				}
				var msg struct {
					Message string `json:"message"`
				}
				if err := json.Unmarshal(resp.Body, &msg); err != nil || msg.Message == "" {
					t.Errorf("403 body %s has no message for the UI", resp.Body)
				}
			})
		}
	}
}

// Before the Connect wizard ran there may be no webhook service account, so
// its login could still be free for a user to take: the webhook is closed.
func TestWebhookNeedsConnect(t *testing.T) {
	app, _ := newTestApp(t)
	webhookSA := &backend.User{Login: "sa-1-pushward-alerts-webhook", Role: "Viewer"}
	resp := callResourceAs(t, context.Background(), app, webhookSA, http.MethodPost, "webhook", []byte(`{"alerts":[]}`))
	if resp.Status != http.StatusForbidden {
		t.Errorf("webhook without a webhook token = %d, want 403", resp.Status)
	}
}

// A webhook that finds every slot taken is answered 503, so Grafana sends it
// again instead of taking a 200 for an alert nobody processed.
func TestWebhookBusyAnswers503(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusCreated)
	}))
	inst, err := NewApp(context.Background(), backend.AppInstanceSettings{
		JSONData:                json.RawMessage(fmt.Sprintf(`{"apiUrl":%q}`, srv.URL)),
		DecryptedSecureJSONData: map[string]string{"apiKey": testAPIKey, "webhookToken": "glsa_test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := inst.(*App)
	t.Cleanup(func() {
		close(release)
		app.Dispose()
		srv.Close()
	})

	webhookSA := &backend.User{Login: "sa-1-pushward-alerts-webhook", Role: "Viewer"}
	payload := func(i int) []byte {
		return fmt.Appendf(nil, `{"alerts":[{"status":"firing","fingerprint":"fp","labels":{"alertname":"Alert%d"}}]}`, i)
	}
	// Each alert name waits on its own create, holding its slot.
	i := 0
	for ; ; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // give up at once when no slot is free
		resp := callResourceAs(t, ctx, app, webhookSA, http.MethodPost, "webhook", payload(i))
		if resp.Status == http.StatusServiceUnavailable {
			break
		}
		if resp.Status != http.StatusOK || i > 100 {
			t.Fatalf("webhook %d = %d %s, want 200 until the slots run out", i, resp.Status, resp.Body)
		}
	}
	if i != 16 { // bridge.maxConcurrentWebhooks
		t.Errorf("%d webhooks taken before the 503, want 16", i)
	}
}
