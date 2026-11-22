package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

// A widget engine whose first start PushWard fails is started again, and one
// PushWard refuses for good is not.
func TestWidgetEngineRetriesStart(t *testing.T) {
	minWait, maxWait := widgetRetryMin, widgetRetryMax
	widgetRetryMin, widgetRetryMax = time.Millisecond, time.Millisecond
	t.Cleanup(func() { widgetRetryMin, widgetRetryMax = minWait, maxWait })

	cases := []struct {
		name      string
		first     int // what the first create gets
		publishes bool
	}{
		{"unavailable", http.StatusServiceUnavailable, true},
		{"rate limited", http.StatusTooManyRequests, true},
		{"key refused", http.StatusForbidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var creates atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/widgets" && creates.Add(1) == 1 {
					w.WriteHeader(tc.first)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)

			s, err := LoadSettings(backend.AppInstanceSettings{
				JSONData:                json.RawMessage(`{"datasourceUid":"ds","widgets":[{"slug":"w","name":"W","query":"up"}]}`),
				DecryptedSecureJSONData: map[string]string{"apiKey": testAPIKey, "webhookToken": "glsa_test"},
			})
			if err != nil {
				t.Fatal(err)
			}
			// No retry budget: a failed create comes back at once.
			a := &App{settings: s, pw: pushward.NewClient(srv.URL, testAPIKey, pushward.WithRetryBudget(time.Nanosecond))}
			a.startWidgetEngine(&dsQuerier{app: a})
			t.Cleanup(func() {
				a.widgetCancel()
				a.widgetWG.Wait()
			})

			// Done once it publishes, or gave up without trying again.
			done := func() bool {
				publishing, msg := a.widgetStatus()
				return publishing || strings.HasPrefix(msg, "widget publishing failed: ")
			}
			for deadline := time.Now().Add(5 * time.Second); !done() && time.Now().Before(deadline); {
				time.Sleep(5 * time.Millisecond)
			}
			if publishing, msg := a.widgetStatus(); publishing != tc.publishes {
				t.Fatalf("publishing = %v (%s), want %v", publishing, msg, tc.publishes)
			}
			if !tc.publishes {
				time.Sleep(50 * time.Millisecond) // many retry delays
				if n := creates.Load(); n != 1 {
					t.Errorf("%d creates after a refusal for good, want 1", n)
				}
			}
		})
	}
}

func TestPermanentWidgetError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pushward.HTTPError{StatusCode: http.StatusUnauthorized}, true},
		{&pushward.HTTPError{StatusCode: http.StatusConflict}, true},
		{&pushward.HTTPError{StatusCode: http.StatusRequestTimeout}, false},
		{&pushward.HTTPError{StatusCode: http.StatusTooManyRequests}, false},
		{&pushward.HTTPError{StatusCode: http.StatusBadGateway}, false},
		{errors.New(`widget "w" create failed: per-user widget cap reached`), true},
		{errors.New("dial tcp: connection refused"), false},
	} {
		if got := permanentWidgetError(tc.err); got != tc.want {
			t.Errorf("permanentWidgetError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
