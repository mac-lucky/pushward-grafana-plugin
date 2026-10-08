package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/mac-lucky/pushward-integrations/shared/e2e"
	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

const testAPIKey = "hlk_testkey0000000000000000000000000"

// mockCallResourceResponseSender captures the response from a CallResource call.
type mockCallResourceResponseSender struct {
	response *backend.CallResourceResponse
}

func (s *mockCallResourceResponseSender) Send(response *backend.CallResourceResponse) error {
	s.response = response
	return nil
}

// newTestApp builds an App pointed at a stub PushWard server whose GET /auth/me
// accepts testAPIKey, so the health probe resolves without real network access.
// The stub also 404s GET /me (the wrong path the probe used to call) so the
// regression guard can assert the probe no longer depends on it. It returns the
// app and the stub's base URL.
func newTestApp(t *testing.T) (*App, string) {
	t.Helper()
	app, url, _ := newTestAppWith(t, nil, nil)
	return app, url
}

// newTestAppWith is newTestApp with extra secure settings. Its stub also
// accepts POST /notifications and keeps their bodies, returned by the func;
// refuse, when set, picks a body to answer 422 with that Problem code.
func newTestAppWith(t *testing.T, secure map[string]string, refuse func(body string) string) (*App, string, func() []string) {
	t.Helper()
	var (
		mu     sync.Mutex
		notifs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /me never existed at the gateway; only /auth/me is real.
		if r.URL.Path == "/auth/me" && r.Header.Get("Authorization") == "Bearer "+testAPIKey {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"test"}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/notifications" && r.Header.Get("Authorization") == "Bearer "+testAPIKey {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			notifs = append(notifs, string(body))
			mu.Unlock()
			if refuse != nil {
				if code := refuse(string(body)); code != "" {
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = fmt.Fprintf(w, `{"status":422,"code":%q,"detail":"refused"}`, code)
					return
				}
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"pushed":true}`))
			return
		}
		if r.URL.Path == "/me" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	sec := map[string]string{"apiKey": testAPIKey}
	maps.Copy(sec, secure)
	inst, err := NewApp(context.Background(), backend.AppInstanceSettings{
		JSONData:                json.RawMessage(fmt.Sprintf(`{"apiUrl":%q,"datasourceUid":"prom-uid"}`, srv.URL)),
		DecryptedSecureJSONData: sec,
	})
	if err != nil {
		t.Fatalf("new app: %s", err)
	}
	app, ok := inst.(*App)
	if !ok {
		t.Fatal("inst must be of type *App")
	}
	t.Cleanup(app.Dispose)
	sent := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), notifs...)
	}
	return app, srv.URL, sent
}

func callResource(t *testing.T, app *App, method, path string) *backend.CallResourceResponse {
	t.Helper()
	return callResourceBody(t, app, method, path, nil)
}

func callResourceBody(t *testing.T, app *App, method, path string, body []byte) *backend.CallResourceResponse {
	t.Helper()
	var r mockCallResourceResponseSender
	if err := app.CallResource(context.Background(), &backend.CallResourceRequest{
		Method: method,
		Path:   path,
		Body:   body,
	}, &r); err != nil {
		t.Fatalf("CallResource %s %s: %s", method, path, err)
	}
	if r.response == nil {
		t.Fatalf("no response from CallResource %s %s", method, path)
	}
	return r.response
}

func TestHealthzResource(t *testing.T) {
	app, _ := newTestApp(t)
	resp := callResource(t, app, http.MethodGet, "healthz")
	if resp.Status != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.Status)
	}
	var body struct {
		OK         bool `json:"ok"`
		APIKey     bool `json:"apiKey"`
		Datasource bool `json:"datasource"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode healthz body: %s", err)
	}
	if !body.OK || !body.APIKey || !body.Datasource {
		t.Errorf("healthz = %+v, want all true (valid key + datasource set)", body)
	}
}

func TestHealthzRejectsBadKey(t *testing.T) {
	app, _ := newTestApp(t)
	app.settings.APIKey = "hlk_wrongkey" // stub /me returns 401 for anything else
	resp := callResource(t, app, http.MethodGet, "healthz")
	var body struct {
		APIKey bool `json:"apiKey"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode healthz body: %s", err)
	}
	if body.APIKey {
		t.Error("healthz reported apiKey=true for a rejected key")
	}
}

func TestConfigResource(t *testing.T) {
	app, apiURL := newTestApp(t)
	resp := callResource(t, app, http.MethodGet, "config")
	if resp.Status != http.StatusOK {
		t.Fatalf("config status = %d, want 200", resp.Status)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode config body: %s", err)
	}
	if body["apiUrl"] != apiURL {
		t.Errorf("apiUrl = %v, want %s", body["apiUrl"], apiURL)
	}
	if body["apiKeySet"] != true {
		t.Errorf("apiKeySet = %v, want true", body["apiKeySet"])
	}
	// The secret itself must never be echoed.
	if _, leaked := body["apiKey"]; leaked {
		t.Error("config response leaked apiKey")
	}
	if body["e2eKeyId"] != "" || body["e2eError"] != "" {
		t.Errorf("e2eKeyId/e2eError = %v/%v, want both empty without a key", body["e2eKeyId"], body["e2eError"])
	}
}

func TestConfigResourceE2EKey(t *testing.T) {
	k, err := e2e.ParseKey(testE2EKey)
	if err != nil {
		t.Fatal(err)
	}

	app, _, _ := newTestAppWith(t, map[string]string{"e2eKey": testE2EKey}, nil)
	resp := callResource(t, app, http.MethodGet, "config")
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode config body: %s", err)
	}
	if body["e2eKeyId"] != k.KID() || body["e2eError"] != "" {
		t.Errorf("e2eKeyId/e2eError = %v/%v, want %s and no error", body["e2eKeyId"], body["e2eError"], k.KID())
	}
	if strings.Contains(string(resp.Body), testE2EKey) {
		t.Error("config response leaked the encryption key")
	}

	const bad = "0123456789abcdef"
	app, _, _ = newTestAppWith(t, map[string]string{"e2eKey": bad}, nil)
	for _, path := range []string{"config", "healthz"} {
		resp = callResource(t, app, http.MethodGet, path)
		var body map[string]any
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			t.Fatalf("decode %s body: %s", path, err)
		}
		if e, _ := body["e2eError"].(string); e == "" {
			t.Errorf("%s e2eError is empty for an invalid key", path)
		}
		if strings.Contains(string(resp.Body), bad) {
			t.Errorf("%s response repeats the invalid key", path)
		}
	}
}

func TestTestNotificationIsSealed(t *testing.T) {
	k, err := e2e.ParseKey(testE2EKey)
	if err != nil {
		t.Fatal(err)
	}
	app, _, sent := newTestAppWith(t, map[string]string{"e2eKey": testE2EKey}, nil)
	resp := callResourceBody(t, app, http.MethodPost, "test", []byte(`{"kind":"notification"}`))
	if resp.Status != http.StatusOK {
		t.Fatalf("test status = %d (%s), want 200", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), k.KID()) {
		t.Errorf("test response %s does not name the Key ID", resp.Body)
	}
	notifs := sent()
	if len(notifs) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notifs))
	}
	var req struct {
		Title     string `json:"title"`
		Encrypted string `json:"encrypted"`
	}
	if err := json.Unmarshal([]byte(notifs[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req.Title != "" {
		t.Errorf("title %q sent in the clear", req.Title)
	}
	if m, err := e2e.Open(k, req.Encrypted); err != nil || m.Title != "PushWard test" {
		t.Errorf("open = %+v, %v; want the test notification", m, err)
	}

	// An invalid key fails the test rather than sending anything.
	app, _, sent = newTestAppWith(t, map[string]string{"e2eKey": "nope"}, nil)
	resp = callResourceBody(t, app, http.MethodPost, "test", []byte(`{"kind":"notification"}`))
	if resp.Status != http.StatusBadRequest || !strings.Contains(string(resp.Body), "encryption key invalid") {
		t.Errorf("test with an invalid key = %d %s, want 400 naming the key", resp.Status, resp.Body)
	}
	if n := len(sent()); n != 0 {
		t.Errorf("got %d notifications with an invalid key, want none", n)
	}
}

func TestStatsResource(t *testing.T) {
	app, _ := newTestApp(t)
	app.metrics.IncPushesSent()

	resp := callResource(t, app, http.MethodGet, "stats")
	if resp.Status != http.StatusOK {
		t.Fatalf("stats status = %d, want 200", resp.Status)
	}
	var body BridgeStats
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode stats body: %s", err)
	}
	if body != app.metrics.Snapshot() {
		t.Errorf("stats = %+v, want %+v", body, app.metrics.Snapshot())
	}
	if body.PushesSent < 1 {
		t.Errorf("PushesSent = %v, want at least the one push recorded above", body.PushesSent)
	}
}

func TestStatsRejectsPost(t *testing.T) {
	app, _ := newTestApp(t)
	resp := callResource(t, app, http.MethodPost, "stats")
	if resp.Status != http.StatusMethodNotAllowed {
		t.Errorf("POST stats status = %d, want 405", resp.Status)
	}
}

func TestUnknownResource404(t *testing.T) {
	app, _ := newTestApp(t)
	resp := callResource(t, app, http.MethodGet, "not_found")
	if resp.Status != http.StatusNotFound {
		t.Errorf("unknown resource status = %d, want 404", resp.Status)
	}
}

func TestTestNotificationOrgKey(t *testing.T) {
	encrypted := func(body string) string {
		if strings.Contains(body, `"encrypted"`) {
			return pushward.ErrCodeNotificationEncryptionUnavailable
		}
		return ""
	}
	app, _, sent := newTestAppWith(t, map[string]string{"e2eKey": testE2EKey}, encrypted)
	resp := callResourceBody(t, app, http.MethodPost, "test", []byte(`{"kind":"notification"}`))
	if resp.Status != http.StatusBadRequest || !strings.Contains(string(resp.Body), "organization") {
		t.Errorf("test with an organization key = %d %s, want 400 naming organization keys", resp.Status, resp.Body)
	}
	// The test reports the problem; it does not fall back to a content-free send.
	if n := len(sent()); n != 1 {
		t.Errorf("got %d sends, want only the refused encrypted one", n)
	}
}
