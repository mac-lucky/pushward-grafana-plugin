package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/testutil"
	"github.com/mac-lucky/pushward-integrations/shared/text"
)

func retractConfig() Config {
	cfg := ackConfig()
	cfg.RetractOnResolve = true
	return cfg
}

func trackedHighCPU(fingerprints ...string) *alertState {
	fps := make(map[string]struct{}, len(fingerprints))
	for _, fp := range fingerprints {
		fps[fp] = struct{}{}
	}
	// lastValues: the timeline's ENDED update needs a value.
	return &alertState{slug: makeSlug("HighCPU"), alertname: "HighCPU", fingerprints: fps, lastSeen: time.Now(), lastValues: map[string]float64{"A": 1}}
}

// trackHighCPU tracks HighCPU with its Live Activity on the mock, so the
// ENDED update that ends it succeeds.
func trackHighCPU(t *testing.T, b *Bridge, fingerprints ...string) {
	t.Helper()
	if err := b.pwClient.CreateActivity(context.Background(), makeSlug("HighCPU"), "HighCPU", 5, 0, 0); err != nil {
		t.Fatal(err)
	}
	b.active["HighCPU"] = trackedHighCPU(fingerprints...)
}

// TestRetractOnEveryEnd covers each path that ends an alert whose push went
// out: the push is taken back under its collapse id, after the acknowledge
// repeats are stopped, and no resolved push follows.
func TestRetractOnEveryEnd(t *testing.T) {
	slug := makeSlug("HighCPU")
	cases := []struct {
		name string
		end  func(t *testing.T, b *Bridge)
	}{
		{"last instance resolves", func(t *testing.T, b *Bridge) {
			trackHighCPU(t, b, "fp1")
			b.handleResolved(context.Background(), resolvedAlert(), nil)
		}},
		{"alert without a Live Activity resolves, also notify off", func(t *testing.T, b *Bridge) {
			b.cfg.AlsoNotify = false
			st := trackedHighCPU("fp1")
			st.notifyOnly = true
			b.active["HighCPU"] = st
			b.handleResolved(context.Background(), resolvedAlert(), nil)
		}},
		{"alertmanager backstop", func(t *testing.T, b *Bridge) {
			trackHighCPU(t, b)
			b.endAlertActivity(context.Background(), "HighCPU", trackedHighCPU())
		}},
		{"alertmanager backstop, alert without a Live Activity, also notify off", func(t *testing.T, b *Bridge) {
			b.cfg.AlsoNotify = false
			b.grafanaClient = resolvingResolver{}
			st := trackedHighCPU("fp1")
			st.notifyOnly = true
			b.active["HighCPU"] = st
			b.checkAlertStates(context.Background())
		}},
		{"stale sweep", func(t *testing.T, b *Bridge) {
			st := trackedHighCPU("fp1")
			st.lastSeen = time.Now().Add(-time.Hour)
			b.active["HighCPU"] = st
			b.sweepStale(context.Background(), time.Minute)
		}},
		{"End from the Activities page", func(t *testing.T, b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.Forget(context.Background(), slug)
		}},
		{"resolved webhook for an untracked alert", func(t *testing.T, b *Bridge) {
			b.handleResolved(context.Background(), resolvedAlert(), map[string]bool{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, nil, retractConfig())
			dl := &recLog{}
			b.deliveryLog = dl
			b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false, b.ackEnabled())

			tc.end(t, b)

			retracts, retractIdx := m.retracts(t, b)
			if len(retracts) != 1 || retracts[0]["collapse_id"] != text.SlugHash("grafana", "HighCPU", 6) {
				t.Fatalf("retracts = %v, want one for the alert's collapse id", retracts)
			}
			if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != 1 {
				t.Errorf("got %d notifications, want only the firing one", len(notifs))
			}
			if _, cancelIdx := m.requests(t, http.MethodPost, "/notifications/receipts/cancel"); len(cancelIdx) != 1 || cancelIdx[0] > retractIdx[0] {
				t.Errorf("cancel at %v, retract at %v: want the repeats stopped first", cancelIdx, retractIdx)
			}
			if e := dl.find("retracted"); e == nil || !e.ok || e.detail != "1" {
				t.Errorf("delivery log retracted = %+v, want ok with 1 device", e)
			}
		})
	}
}

// A retract that does not reach falls back to the resolved push at once: the
// retract client does not wait out a 429 on the webhook's budget.
func TestRetractFallsBackToResolved(t *testing.T) {
	zero := 0
	cases := []struct {
		name string
		opts testutil.MockOptions
	}{
		{"no device reached", testutil.MockOptions{RetractDevices: &zero}},
		{"retract failed", testutil.MockOptions{RetractStatus: http.StatusInternalServerError}},
		{"retract throttled", testutil.MockOptions{RetractStatus: http.StatusTooManyRequests}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, tc.opts)
			b := newTestBridge(t, m.url, nil, retractConfig())
			trackHighCPU(t, b, "fp1")

			start := time.Now()
			b.handleResolved(context.Background(), resolvedAlert(), nil)
			if d := time.Since(start); d > retractTimeout/2 {
				t.Errorf("resolve took %s, want the fallback without a retry wait", d)
			}

			retracts, retractIdx := m.retracts(t, b)
			if len(retracts) != 1 {
				t.Fatalf("got %d retracts, want 1", len(retracts))
			}
			notifs, notifIdx := m.requests(t, http.MethodPost, "/notifications")
			if len(notifs) != 1 {
				t.Fatalf("got %d notifications, want one resolved push", len(notifs))
			}
			if body, _ := notifs[0]["body"].(string); !strings.HasPrefix(body, "Resolved") {
				t.Errorf("body = %q, want the resolved push", body)
			}
			if notifIdx[0] < retractIdx[0] {
				t.Error("the resolved push went out before the retract was answered")
			}
		})
	}
}

// When the ENDED update fails, the Live Activity still shows the alert
// firing: the resolved push is what tells the user, so it replaces the firing
// push instead of a retract taking that away.
func TestNoRetractWhileTheActivityShows(t *testing.T) {
	cases := []struct {
		name string
		end  func(b *Bridge)
	}{
		{"resolve", func(b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.handleResolved(context.Background(), resolvedAlert(), nil)
		}},
		{"alertmanager backstop", func(b *Bridge) {
			b.endAlertActivity(context.Background(), "HighCPU", trackedHighCPU())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The mock holds no activity, so the ENDED update is a 404.
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, nil, retractConfig())

			tc.end(b)

			if r, _ := m.retracts(t, b); len(r) != 0 {
				t.Errorf("got %d retracts while the activity still shows, want none", len(r))
			}
			notifs, _ := m.requests(t, http.MethodPost, "/notifications")
			if len(notifs) != 1 {
				t.Fatalf("got %d notifications, want one resolved push", len(notifs))
			}
			if body, _ := notifs[0]["body"].(string); !strings.HasPrefix(body, "Resolved") {
				t.Errorf("body = %q, want the resolved push", body)
			}
		})
	}
}

// node-1 resolves and node-2 starts firing in one payload. A retract would
// land after node-2's firing push and take it too, so the resolve does what it
// does with the switch off.
func TestNoRetractWhenTheAlertFiresInThePayload(t *testing.T) {
	cases := []struct {
		name       string
		tracked    bool
		wantNotifs int
	}{
		{"tracked: resolved push, then firing", true, 2},
		{"untracked: firing push only", false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, nil, retractConfig())
			if tc.tracked {
				trackHighCPU(t, b, "fp1")
			}
			node2 := firingAlert()
			node2.Fingerprint = "fp2"
			raw, err := json.Marshal(webhookPayload{Alerts: []alert{resolvedAlert(), node2}})
			if err != nil {
				t.Fatal(err)
			}

			if err := b.ProcessWebhook(context.Background(), raw); err != nil {
				t.Fatal(err)
			}
			b.wg.Wait()

			if r, _ := m.retracts(t, b); len(r) != 0 {
				t.Errorf("got %d retracts, want none while the alert fires again", len(r))
			}
			notifs, _ := m.requests(t, http.MethodPost, "/notifications")
			if len(notifs) != tc.wantNotifs {
				t.Fatalf("got %d notifications, want %d", len(notifs), tc.wantNotifs)
			}
			if body, _ := notifs[len(notifs)-1]["body"].(string); strings.HasPrefix(body, "Resolved") {
				t.Errorf("last push = %q, want node-2's firing push", body)
			}
		})
	}
}

// After a settings save every instance of an alert resolves untracked; the
// payload retracts their shared collapse id once.
func TestRetractOncePerPayload(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, retractConfig())
	var alerts []alert
	for _, fp := range []string{"fp1", "fp2", "fp3"} {
		a := resolvedAlert()
		a.Fingerprint = fp
		alerts = append(alerts, a)
	}
	raw, err := json.Marshal(webhookPayload{Alerts: alerts})
	if err != nil {
		t.Fatal(err)
	}

	if err := b.ProcessWebhook(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	b.wg.Wait()

	if r, _ := m.retracts(t, b); len(r) != 1 {
		t.Errorf("got %d retracts, want 1 for the alert's collapse id", len(r))
	}
}

// The sweep and End from the Activities page are off the webhook's budget, so
// their retracts run inline: done when the call returns, one at a time, and
// once per collapse id. Acknowledge is off, so no cancel call gives a
// detached retract time to finish.
func TestRetractInlineOffTheWebhook(t *testing.T) {
	cfg := retractConfig()
	cfg.Ack = false
	t.Run("stale sweep", func(t *testing.T) {
		m := newMockServer(t, testutil.MockOptions{})
		b := newTestBridge(t, m.url, nil, cfg)
		old := time.Now().Add(-time.Hour)
		b.active["HighCPU"] = &alertState{slug: makeSlug("HighCPU"), alertname: "HighCPU", lastSeen: old}
		// Two anonymous alerts share the fallback collapse id.
		b.active["fpA"] = &alertState{slug: makeSlug("fpA"), lastSeen: old}
		b.active["fpB"] = &alertState{slug: makeSlug("fpB"), lastSeen: old}

		b.sweepStale(context.Background(), time.Minute)

		if r, _ := m.requests(t, http.MethodPost, "/notifications/retract"); len(r) != 2 {
			t.Errorf("got %d retracts when the sweep returned, want 2 (one per collapse id)", len(r))
		}
	})
	t.Run("End from the Activities page", func(t *testing.T) {
		m := newMockServer(t, testutil.MockOptions{})
		b := newTestBridge(t, m.url, nil, cfg)
		b.active["HighCPU"] = trackedHighCPU("fp1")

		b.Forget(context.Background(), makeSlug("HighCPU"))

		if r, _ := m.requests(t, http.MethodPost, "/notifications/retract"); len(r) != 1 {
			t.Errorf("got %d retracts when Forget returned, want 1", len(r))
		}
	})
}

// With Also send a push notification off, an alert with a Live Activity sent
// no push, so the paths that end it have nothing to retract.
func TestNoRetractWithoutAPush(t *testing.T) {
	slug := makeSlug("HighCPU")
	cases := []struct {
		name string
		end  func(b *Bridge)
	}{
		{"stale sweep", func(b *Bridge) {
			st := trackedHighCPU("fp1")
			st.lastSeen = time.Now().Add(-time.Hour)
			b.active["HighCPU"] = st
			b.sweepStale(context.Background(), time.Minute)
		}},
		{"End from the Activities page", func(b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.Forget(context.Background(), slug)
		}},
		{"resolved webhook for an untracked alert", func(b *Bridge) {
			b.handleResolved(context.Background(), resolvedAlert(), map[string]bool{})
		}},
		{"resolve overtakes the firing push", func(b *Bridge) {
			b.cancelAckIfGone(context.Background(), slog.Default(), "HighCPU", "HighCPU", slug, false)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			cfg := retractConfig()
			cfg.AlsoNotify = false
			b := newTestBridge(t, m.url, nil, cfg)

			tc.end(b)

			if r, _ := m.retracts(t, b); len(r) != 0 {
				t.Errorf("got %d retracts for an alert that sent no push, want none", len(r))
			}
		})
	}
}

func TestRetractNotWhileOtherInstancesFire(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, retractConfig())
	b.active["HighCPU"] = trackedHighCPU("fp1", "fp2")

	b.handleResolved(context.Background(), resolvedAlert(), nil)

	if r, _ := m.retracts(t, b); len(r) != 0 {
		t.Errorf("got %d retracts while an instance still fires, want none", len(r))
	}
}

// TestRetractWhenResolveOvertakesFiringPush covers cancelAckIfGone: the
// resolve runs while the firing push is still pending, so the push lands after
// the resolve and has to be taken back. Acknowledge is off to show the
// retract does not depend on it.
func TestRetractWhenResolveOvertakesFiringPush(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	var b *Bridge
	res := resolvingResolver{resolve: func() { b.handleResolved(context.Background(), resolvedAlert(), nil) }}
	cfg := retractConfig()
	cfg.Ack = false
	b = newTestBridge(t, m.url, res, cfg)

	a := firingAlert()
	a.GeneratorURL = "http://grafana/alerting/grafana/rule-1/view"
	b.handleFiring(context.Background(), a, nil)

	// The resolve came before the activity existed, so it sent the resolved
	// push; the firing push went out last.
	notifs, notifIdx := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) == 0 {
		t.Fatal("no firing push")
	}
	firing := notifIdx[len(notifIdx)-1]
	retracts, retractIdx := m.retracts(t, b)
	if len(retracts) == 0 || retractIdx[len(retractIdx)-1] < firing {
		t.Fatalf("retracts at %v, want one after the firing push at %d", retractIdx, firing)
	}
	if cancels, _ := m.requests(t, http.MethodPost, "/notifications/receipts/cancel"); len(cancels) != 0 {
		t.Errorf("got %d cancel requests with acknowledge off, want none", len(cancels))
	}
}
