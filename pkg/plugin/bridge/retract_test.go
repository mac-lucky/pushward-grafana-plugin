package bridge

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
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
	return &alertState{slug: makeSlug("HighCPU"), alertname: "HighCPU", fingerprints: fps, lastSeen: time.Now()}
}

// TestRetractOnEveryEnd covers each path that ends an alert whose push went
// out: the push is taken back under its collapse id, after the acknowledge
// repeats are stopped, and no resolved push follows.
func TestRetractOnEveryEnd(t *testing.T) {
	slug := makeSlug("HighCPU")
	cases := []struct {
		name string
		end  func(b *Bridge)
	}{
		{"last instance resolves", func(b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.handleResolved(context.Background(), resolvedAlert())
		}},
		{"alert without a Live Activity resolves, also notify off", func(b *Bridge) {
			b.cfg.AlsoNotify = false
			st := trackedHighCPU("fp1")
			st.notifyOnly = true
			b.active["HighCPU"] = st
			b.handleResolved(context.Background(), resolvedAlert())
		}},
		{"alertmanager backstop", func(b *Bridge) {
			b.endAlertActivity(context.Background(), "HighCPU", &alertState{slug: slug, alertname: "HighCPU"})
		}},
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
			b.handleResolved(context.Background(), resolvedAlert())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, nil, retractConfig())
			dl := &recLog{}
			b.deliveryLog = dl
			b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false, b.ackEnabled())

			tc.end(b)

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

func TestRetractFallsBackToResolved(t *testing.T) {
	zero := 0
	cases := []struct {
		name string
		opts testutil.MockOptions
	}{
		{"no device reached", testutil.MockOptions{RetractDevices: &zero}},
		{"retract failed", testutil.MockOptions{RetractStatus: http.StatusInternalServerError}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, tc.opts)
			b := newTestBridge(t, m.url, nil, retractConfig())
			// No retries, so the 500 is one request too.
			b.pwClient = pushward.NewClient(m.url, "hlk_x", pushward.WithRetryBudget(time.Nanosecond))
			b.active["HighCPU"] = trackedHighCPU("fp1")

			b.handleResolved(context.Background(), resolvedAlert())

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

func TestRetractNotWhileOtherInstancesFire(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, retractConfig())
	b.active["HighCPU"] = trackedHighCPU("fp1", "fp2")

	b.handleResolved(context.Background(), resolvedAlert())

	if r, _ := m.retracts(t, b); len(r) != 0 {
		t.Errorf("got %d retracts while an instance still fires, want none", len(r))
	}
}

// TestRetractWhenResolveOvertakesFiringPush covers cancelAckIfGone: the
// resolve runs while the firing push is still pending, so the push lands after
// the resolve's retract and has to be taken back once more. Acknowledge is off
// to show the retract does not depend on it.
func TestRetractWhenResolveOvertakesFiringPush(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	var b *Bridge
	res := resolvingResolver{resolve: func() { b.handleResolved(context.Background(), resolvedAlert()) }}
	cfg := retractConfig()
	cfg.Ack = false
	b = newTestBridge(t, m.url, res, cfg)

	a := firingAlert()
	a.GeneratorURL = "http://grafana/alerting/grafana/rule-1/view"
	b.handleFiring(context.Background(), a, nil)

	notifs, notifIdx := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 1 {
		t.Fatalf("got %d notifications, want only the firing push", len(notifs))
	}
	retracts, retractIdx := m.retracts(t, b)
	if len(retracts) == 0 || retractIdx[len(retractIdx)-1] < notifIdx[0] {
		t.Fatalf("retracts at %v, want one after the firing push at %d", retractIdx, notifIdx[0])
	}
	if cancels, _ := m.requests(t, http.MethodPost, "/notifications/receipts/cancel"); len(cancels) != 0 {
		t.Errorf("got %d cancel requests with acknowledge off, want none", len(cancels))
	}
}
