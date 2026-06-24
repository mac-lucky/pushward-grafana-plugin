package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// mockCallResourceResponseSender captures the response from a CallResource call.
type mockCallResourceResponseSender struct {
	response *backend.CallResourceResponse
}

func (s *mockCallResourceResponseSender) Send(response *backend.CallResourceResponse) error {
	s.response = response
	return nil
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	inst, err := NewApp(context.Background(), backend.AppInstanceSettings{
		JSONData: json.RawMessage(`{"apiUrl":"https://api.pushward.app","datasourceUid":"prom-uid"}`),
		DecryptedSecureJSONData: map[string]string{
			"apiKey": "hlk_testkey0000000000000000000000000",
		},
	})
	if err != nil {
		t.Fatalf("new app: %s", err)
	}
	app, ok := inst.(*App)
	if !ok {
		t.Fatal("inst must be of type *App")
	}
	t.Cleanup(app.Dispose)
	return app
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
	app := newTestApp(t)
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
		t.Errorf("healthz = %+v, want all true", body)
	}
}

func TestConfigResource(t *testing.T) {
	app := newTestApp(t)
	resp := callResource(t, app, http.MethodGet, "config")
	if resp.Status != http.StatusOK {
		t.Fatalf("config status = %d, want 200", resp.Status)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode config body: %s", err)
	}
	if body["apiUrl"] != "https://api.pushward.app" {
		t.Errorf("apiUrl = %v, want https://api.pushward.app", body["apiUrl"])
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
	app := newTestApp(t)
	resp := callResource(t, app, http.MethodGet, "not_found")
	if resp.Status != http.StatusNotFound {
		t.Errorf("unknown resource status = %d, want 404", resp.Status)
	}
}
