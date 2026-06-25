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

const testAPIKey = "hlk_testkey0000000000000000000000000"

// mockCallResourceResponseSender captures the response from a CallResource call.
type mockCallResourceResponseSender struct {
	response *backend.CallResourceResponse
}

func (s *mockCallResourceResponseSender) Send(response *backend.CallResourceResponse) error {
	s.response = response
	return nil
}

// newTestApp builds an App pointed at a stub PushWard server whose GET /me
// accepts testAPIKey, so the health probe resolves without real network access.
// It returns the app and the stub's base URL.
func newTestApp(t *testing.T) (*App, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me" && r.Header.Get("Authorization") == "Bearer "+testAPIKey {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"test"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	inst, err := NewApp(context.Background(), backend.AppInstanceSettings{
		JSONData:                json.RawMessage(fmt.Sprintf(`{"apiUrl":%q,"datasourceUid":"prom-uid"}`, srv.URL)),
		DecryptedSecureJSONData: map[string]string{"apiKey": testAPIKey},
	})
	if err != nil {
		t.Fatalf("new app: %s", err)
	}
	app, ok := inst.(*App)
	if !ok {
		t.Fatal("inst must be of type *App")
	}
	t.Cleanup(app.Dispose)
	return app, srv.URL
}

func callResource(t *testing.T, app *App, method, path string) *backend.CallResourceResponse {
	t.Helper()
	var r mockCallResourceResponseSender
	if err := app.CallResource(context.Background(), &backend.CallResourceRequest{
		Method: method,
		Path:   path,
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
}

func TestUnknownResource404(t *testing.T) {
	app, _ := newTestApp(t)
	resp := callResource(t, app, http.MethodGet, "not_found")
	if resp.Status != http.StatusNotFound {
		t.Errorf("unknown resource status = %d, want 404", resp.Status)
	}
}
