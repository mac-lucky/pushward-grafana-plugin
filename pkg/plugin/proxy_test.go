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

// newAppWithStub builds an App whose PushWard base URL points at the given stub
// handler, so list/proxy/probe behavior can be asserted without real network.
func newAppWithStub(t *testing.T, handler http.HandlerFunc) *App {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	inst, err := NewApp(context.Background(), backend.AppInstanceSettings{
		JSONData:                json.RawMessage(fmt.Sprintf(`{"apiUrl":%q}`, srv.URL)),
		DecryptedSecureJSONData: map[string]string{"apiKey": testAPIKey},
	})
	if err != nil {
		t.Fatalf("new app: %s", err)
	}
	app := inst.(*App)
	t.Cleanup(app.Dispose)
	return app
}

// TestProbeUsesAuthMeNotMe is the regression guard for the health-probe path
// bug: the probe must hit /auth/me (which an hlk_ key resolves), and a 404 on
// the old /me path must not make a valid key look rejected.
func TestProbeUsesAuthMeNotMe(t *testing.T) {
	var hitMe, hitAuthMe bool
	app := newAppWithStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			hitMe = true
			w.WriteHeader(http.StatusNotFound)
		case "/auth/me":
			hitAuthMe = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"x"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})

	status, _ := app.probeAPIKey(context.Background())
	if status != probeValid {
		t.Errorf("probe status = %d, want probeValid", status)
	}
	if hitMe {
		t.Error("probe must not call the non-existent /me path")
	}
	if !hitAuthMe {
		t.Error("probe must call /auth/me")
	}
}

// TestProbeTriState confirms a 5xx/404 is reported as unknown (not rejected),
// while 401/403 is rejected.
func TestProbeTriState(t *testing.T) {
	cases := []struct {
		code int
		want probeStatus
	}{
		{http.StatusOK, probeValid},
		{http.StatusUnauthorized, probeRejected},
		{http.StatusForbidden, probeRejected},
		{http.StatusNotFound, probeUnknown},
		{http.StatusInternalServerError, probeUnknown},
		{http.StatusTooManyRequests, probeUnknown},
	}
	for _, tc := range cases {
		code := tc.code
		app := newAppWithStub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/auth/me" {
				w.WriteHeader(code)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		})
		if got, _ := app.probeAPIKey(context.Background()); got != tc.want {
			t.Errorf("status %d: probe = %d, want %d", code, got, tc.want)
		}
	}
}

// TestActivitiesFollowsPagination guards the cursor-pagination fix: the server
// returns {items,next_cursor}; the plugin must follow the `after` cursor across
// every page and hand the frontend the full concatenated array, never just the
// first page.
func TestActivitiesFollowsPagination(t *testing.T) {
	app := newAppWithStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/activities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("after") {
		case "": // first page: more to come
			_, _ = w.Write([]byte(`{"items":[{"slug":"a"},{"slug":"b"}],"next_cursor":"p2"}`))
		case "p2": // final page: empty cursor stops the loop
			_, _ = w.Write([]byte(`{"items":[{"slug":"c"},{"slug":"d"}],"next_cursor":""}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	resp := callResource(t, app, http.MethodGet, "activities")
	var body struct {
		Activities []map[string]any `json:"activities"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode: %s (body=%s)", err, resp.Body)
	}
	if len(body.Activities) != 4 {
		t.Fatalf("activities = %d, want 4 items concatenated across 2 pages", len(body.Activities))
	}
}

// TestWidgetsEndpointUnwrapsEnvelope guards the new /widgets proxy.
func TestWidgetsEndpointUnwrapsEnvelope(t *testing.T) {
	app := newAppWithStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/widgets" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"items":[{"slug":"w1"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	resp := callResource(t, app, http.MethodGet, "widgets")
	var body struct {
		Widgets []map[string]any `json:"widgets"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode: %s", err)
	}
	if len(body.Widgets) != 1 {
		t.Fatalf("widgets = %v, want 1", body.Widgets)
	}
}

// TestListProxyEmptyEnvelopeYieldsArray ensures an empty/odd upstream body still
// produces a JSON array, never null, so the frontend .map never throws.
func TestListProxyEmptyEnvelopeYieldsArray(t *testing.T) {
	app := newAppWithStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":null}`))
	})
	resp := callResource(t, app, http.MethodGet, "activities")
	var body struct {
		Activities []map[string]any `json:"activities"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode: %s", err)
	}
	if body.Activities == nil {
		// json [] decodes to a non-nil empty slice; null would decode to nil.
		t.Error("expected an empty array, got null")
	}
}

// TestReadEndpointsRejectNonGet guards the method-guard fix on read endpoints.
func TestReadEndpointsRejectNonGet(t *testing.T) {
	app := newAppWithStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	for _, path := range []string{"healthz", "config", "activities", "widgets", "history"} {
		resp := callResource(t, app, http.MethodPost, path)
		if resp.Status != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, resp.Status)
		}
	}
}
