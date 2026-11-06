package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// The sweep and End from the Activities page queue their retracts, once per
// collapse id. Acknowledge is off, so no cancel call comes in between.
func TestRetractQueuedOncePerCollapseID(t *testing.T) {
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

		if n := len(b.retracts); n != 2 {
			t.Errorf("%d retracts queued when the sweep returned, want 2 (one per collapse id)", n)
		}
		if r, _ := m.retracts(t, b); len(r) != 2 {
			t.Errorf("got %d retracts, want 2", len(r))
		}
	})
	t.Run("End from the Activities page", func(t *testing.T) {
		m := newMockServer(t, testutil.MockOptions{})
		b := newTestBridge(t, m.url, nil, cfg)
		b.active["HighCPU"] = trackedHighCPU("fp1")

		b.Forget(context.Background(), makeSlug("HighCPU"))

		if r, _ := m.retracts(t, b); len(r) != 1 {
			t.Errorf("got %d retracts, want 1", len(r))
		}
	})
}

// With Also send a push notification off, an alert with a Live Activity sent
// no push, so the paths that end it have nothing to retract. A resolve that
// overtakes the firing push needs that push, so it has no case here.
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

// Grafana sends the resolved webhook at the group's next flush, minutes after
// the alertmanager backstop, the stale sweep or End from the Activities page
// ended the alert. That late resolve finds the alert untracked and must leave
// the devices as the end left them.
func TestLateResolveAfterTheAlertEnded(t *testing.T) {
	cases := []struct {
		name         string
		end          func(t *testing.T, b *Bridge)
		wantRetracts int
		wantResolved bool
	}{
		{"backstop, Live Activity not ended: the resolved push stays", func(t *testing.T, b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1") // no activity on the mock, ENDED fails
			b.checkAlertStates(context.Background())
		}, 0, true},
		{"backstop, Live Activity ended: one retract", func(t *testing.T, b *Bridge) {
			trackHighCPU(t, b, "fp1")
			b.checkAlertStates(context.Background())
		}, 1, false},
		{"stale sweep: one retract", func(t *testing.T, b *Bridge) {
			st := trackedHighCPU("fp1")
			st.lastSeen = time.Now().Add(-time.Hour)
			b.active["HighCPU"] = st
			b.sweepStale(context.Background(), time.Minute)
		}, 1, false},
		{"End from the Activities page: one retract", func(t *testing.T, b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.Forget(context.Background(), makeSlug("HighCPU"))
		}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, resolvingResolver{}, retractConfig())
			slug := makeSlug("HighCPU")
			b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false, b.ackEnabled())
			tc.end(t, b)
			m.retracts(t, b)

			raw, err := json.Marshal(webhookPayload{Alerts: []alert{resolvedAlert()}})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.ProcessWebhook(context.Background(), raw); err != nil {
				t.Fatal(err)
			}
			b.wg.Wait()

			if r, _ := m.retracts(t, b); len(r) != tc.wantRetracts {
				t.Errorf("got %d retracts, want %d", len(r), tc.wantRetracts)
			}
			notifs, _ := m.requests(t, http.MethodPost, "/notifications")
			last, _ := notifs[len(notifs)-1]["body"].(string)
			if got := strings.HasPrefix(last, "Resolved"); got != tc.wantResolved {
				t.Errorf("last push = %q, want the resolved push: %v", last, tc.wantResolved)
			}
		})
	}
}

// The alert fires again after the backstop ended it: its next resolve takes
// the new push back.
func TestFiringPushAfterAnEndIsRetracted(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	cfg := retractConfig()
	cfg.ActivityMode = ActivityModeLabeled // no Live Activity, push only
	b := newTestBridge(t, m.url, resolvingResolver{}, cfg)
	st := trackedHighCPU("fp1")
	st.notifyOnly = true
	b.active["HighCPU"] = st
	b.checkAlertStates(context.Background())

	b.handleFiring(context.Background(), firingAlert(), nil)
	b.handleResolved(context.Background(), resolvedAlert(), nil)

	if r, _ := m.retracts(t, b); len(r) != 2 {
		t.Errorf("got %d retracts, want 2: one per end", len(r))
	}
}

// One payload retracts a collapse id once, and sends nothing under it after:
// the retract lands late and would take that push too.
func TestRetractOnceAndNothingAfterInAPayload(t *testing.T) {
	anon := func(fp string) alert {
		return alert{Status: alertStatusResolved, Fingerprint: fp, Labels: map[string]string{}}
	}
	trackAnon := func(b *Bridge, fp string) {
		b.active[fp] = &alertState{slug: makeSlug(fp), notifyOnly: true, fingerprints: map[string]struct{}{fp: {}}, lastSeen: time.Now()}
	}
	second := resolvedAlert()
	second.Fingerprint = "fp2"
	cases := []struct {
		name   string
		track  func(b *Bridge)
		alerts []alert
	}{
		{"three tracked alerts without a name", func(b *Bridge) {
			trackAnon(b, "fpA")
			trackAnon(b, "fpB")
			trackAnon(b, "fpC")
		}, []alert{anon("fpA"), anon("fpB"), anon("fpC")}},
		{"last instance, then an untracked one of the same alert", func(b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.active["HighCPU"].notifyOnly = true
		}, []alert{resolvedAlert(), second}},
		{"untracked alert without a name, then a tracked one", func(b *Bridge) {
			trackAnon(b, "fpB")
		}, []alert{anon("fpA"), anon("fpB")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, nil, retractConfig())
			tc.track(b)
			raw, err := json.Marshal(webhookPayload{Alerts: tc.alerts})
			if err != nil {
				t.Fatal(err)
			}

			if err := b.ProcessWebhook(context.Background(), raw); err != nil {
				t.Fatal(err)
			}
			b.wg.Wait()

			if r, _ := m.retracts(t, b); len(r) != 1 {
				t.Errorf("got %d retracts, want 1", len(r))
			}
			if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != 0 {
				t.Errorf("got %d pushes, want none under a retracted id", len(notifs))
			}
		})
	}
}

// newQueueBridge is a bridge from NewBridge, so its retract queue runs. No
// sweeper (StaleTimeout 0) and no backstop (no resolver) run beside it.
func newQueueBridge(t *testing.T, url string, cfg Config) *Bridge {
	t.Helper()
	pw := pushward.NewClient(url, "hlk_x")
	rc := pushward.NewClient(url, "hlk_x", pushward.WithRetryBudget(time.Nanosecond))
	b := NewBridge(pw, rc, nopQuerier{}, nil, NewPoller(nopQuerier{}, pw, time.Hour), nil, nil, cfg)
	t.Cleanup(b.Stop)
	return b
}

// throttledRetracts serves retracts the way the account's rate limit does past
// its burst: after the first burst, every other attempt is a 429 with a short
// retry_after_ms. accepted counts the retracts that went through.
func throttledRetracts(t *testing.T, burst int) (url string, accepted func() int) {
	t.Helper()
	var mu sync.Mutex
	var calls, ok int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/notifications/retract" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		calls++
		pass := calls <= burst || (calls-burst)%2 == 0
		if pass {
			ok++
		}
		mu.Unlock()
		if !pass {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status":429,"code":"rate_limit.exceeded","retry_after_ms":5}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"devices":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int {
		mu.Lock()
		defer mu.Unlock()
		return ok
	}
}

// Ends without a resolved push to fall back on can take more pushes back
// than the burst of 5: the queue waits out each 429 and gets them all down.
func TestQueuedRetractsWaitOutTheRateLimit(t *testing.T) {
	const n = 8
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("Alert%d", i)
	}
	cases := []struct {
		name string
		end  func(t *testing.T, b *Bridge)
	}{
		{"stale sweep", func(t *testing.T, b *Bridge) {
			old := time.Now().Add(-time.Hour)
			b.mu.Lock()
			for _, name := range names {
				b.active[name] = &alertState{slug: makeSlug(name), alertname: name, lastSeen: old}
			}
			b.mu.Unlock()
			b.sweepStale(context.Background(), time.Minute)
		}},
		{"untracked resolves after a restart", func(t *testing.T, b *Bridge) {
			var alerts []alert
			for i, name := range names {
				alerts = append(alerts, alert{Status: alertStatusResolved, Fingerprint: fmt.Sprint("fp", i), Labels: map[string]string{"alertname": name}})
			}
			raw, err := json.Marshal(webhookPayload{Alerts: alerts})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.ProcessWebhook(context.Background(), raw); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, accepted := throttledRetracts(t, 5)
			cfg := retractConfig()
			cfg.Ack = false // no cancel calls; the server only takes retracts
			b := newQueueBridge(t, url, cfg)

			tc.end(t, b)

			deadline := time.Now().Add(5 * time.Second)
			for accepted() < n && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := accepted(); got != n {
				t.Errorf("%d of %d retracts went through, want all", got, n)
			}
		})
	}
}

// Stop does not sit out a Retry-After: the retract waiting on it is given up.
func TestStopDoesNotWaitOutTheRateLimit(t *testing.T) {
	asked := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case asked <- struct{}{}:
		default:
		}
		w.Header().Set("Retry-After", "60")
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status":429,"code":"rate_limit.exceeded"}`))
	}))
	t.Cleanup(srv.Close)
	b := newQueueBridge(t, srv.URL, retractConfig())

	endAndQueue(b, "HighCPU")
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued retract was never made")
	}
	start := time.Now()
	b.Stop()
	if d := time.Since(start); d > time.Second {
		t.Errorf("Stop took %s, want it to give up the throttled retract", d)
	}
}

func TestRetractQueueFull(t *testing.T) {
	b := newTestBridge(t, "http://127.0.0.1:0", nil, retractConfig())
	dl := &recLog{}
	b.deliveryLog = dl
	for range retractQueueCap + 1 {
		endAndQueue(b, "HighCPU")
	}
	if n := len(b.retracts); n != retractQueueCap {
		t.Errorf("%d retracts queued, want %d", n, retractQueueCap)
	}
	if e := dl.find("retract"); e == nil || e.ok {
		t.Errorf("delivery log retract = %+v, want the dropped one logged as failed", e)
	}
}

// The memory of ended ids is capped; the oldest is forgotten first.
func TestEndedIsBounded(t *testing.T) {
	b := newTestBridge(t, "http://127.0.0.1:0", nil, retractConfig())
	base := time.Now().Add(-time.Hour)
	for i := range activeAlertCap {
		b.ended[fmt.Sprint(i)] = base.Add(time.Duration(i) * time.Second)
	}

	if !b.endedOnce("new") {
		t.Fatal("endedOnce(new) = false, want true")
	}
	if b.endedOnce("new") {
		t.Error("endedOnce(new) again = true, want false")
	}
	if len(b.ended) != activeAlertCap {
		t.Errorf("len(ended) = %d, want %d", len(b.ended), activeAlertCap)
	}
	if _, ok := b.ended["0"]; ok {
		t.Error("the oldest id survived")
	}
}

// endAndQueue ends alertname's pushes and queues their retract, as the paths
// without a resolved push do.
func endAndQueue(b *Bridge, alertname string) {
	b.mu.Lock()
	b.endedOnce(alertCollapseID(alertname))
	b.mu.Unlock()
	b.queueRetract(slog.Default(), alertname, makeSlug(alertname))
}

// A queued retract never goes out after the alert fired again under its
// collapse id: it would take the new push down. Neither one still waiting in
// the queue nor one waiting out a 429.
func TestQueuedRetractSparesAReFiredAlert(t *testing.T) {
	refire := func(b *Bridge) {
		slug := makeSlug("HighCPU")
		b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false, false)
	}

	t.Run("still queued", func(t *testing.T) {
		m := newMockServer(t, testutil.MockOptions{})
		b := newTestBridge(t, m.url, nil, retractConfig())
		endAndQueue(b, "HighCPU")
		refire(b)
		if r, _ := m.retracts(t, b); len(r) != 0 {
			t.Errorf("got %d retracts after the alert fired again, want none", len(r))
		}
	})

	t.Run("waiting out a 429", func(t *testing.T) {
		url, asked, events := throttledOnce(t)
		cfg := retractConfig()
		cfg.Ack = false
		b := newQueueBridge(t, url, cfg)

		endAndQueue(b, "HighCPU")
		<-asked
		refire(b)
		time.Sleep(time.Second) // past the Retry-After
		if got := events(); got != "retract 429, push" {
			t.Errorf("server saw %q, want no retract after the push", got)
		}
	})

	// The alert fires and ends again while the first retract waits: that one
	// is stopped, and the second end still takes the new push back.
	t.Run("ended again while one waits", func(t *testing.T) {
		url, asked, events := throttledOnce(t)
		cfg := retractConfig()
		cfg.Ack = false
		b := newQueueBridge(t, url, cfg)

		endAndQueue(b, "HighCPU")
		<-asked
		id := alertCollapseID("HighCPU")
		b.mu.Lock() // the push and the end, before the first retract returns
		delete(b.ended, id)
		b.cancelRetract(errFiredAgain)
		b.endedOnce(id)
		b.mu.Unlock()
		b.queueRetract(slog.Default(), "HighCPU", makeSlug("HighCPU"))

		deadline := time.Now().Add(5 * time.Second)
		for events() != "retract 429, retract" && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := events(); got != "retract 429, retract" {
			t.Errorf("server saw %q, want the second end's retract", got)
		}
	})
}

// An alert with a Live Activity fires again while its end's retract waits
// out a 429: its firing push only follows the activity create, and the
// retract must not go out in between. The create is held for as long as
// that retract runs, so without the stop the retract always goes out first.
func TestQueuedRetractStopsWhenTheAlertFiresAgainBeforeItsPush(t *testing.T) {
	var bp atomic.Pointer[Bridge]
	var mu sync.Mutex
	var seen []string
	ev := func(s string) {
		mu.Lock()
		seen = append(seen, s)
		mu.Unlock()
	}
	ask := make(chan struct{})
	retracts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/activities":
			ev("create")
			for b := bp.Load(); ; time.Sleep(5 * time.Millisecond) {
				b.mu.Lock()
				busy := b.retracting != ""
				b.mu.Unlock()
				if !busy {
					break
				}
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/activities/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/notifications":
			ev("push")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/notifications/retract":
			mu.Lock()
			retracts++
			first := retracts == 1
			mu.Unlock()
			if first {
				ev("retract 429")
				close(ask)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"status":429,"code":"rate_limit.exceeded","retry_after_ms":300}`))
				return
			}
			ev("retract")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"devices":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := retractConfig()
	cfg.Ack = false
	b := newQueueBridge(t, srv.URL, cfg)
	bp.Store(b)

	b.mu.Lock()
	b.active["HighCPU"] = trackedHighCPU("fp1")
	b.mu.Unlock()
	b.Forget(context.Background(), makeSlug("HighCPU"))
	<-ask

	raw, _ := json.Marshal(webhookPayload{Alerts: []alert{firingAlert()}})
	if err := b.ProcessWebhook(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	b.wg.Wait()
	mu.Lock()
	got := strings.Join(seen, ", ")
	mu.Unlock()
	if got != "retract 429, create, push" {
		t.Errorf("server saw %q, want the waiting retract stopped before the push", got)
	}
}

// throttledOnce answers the first retract with a 429 asking for 300 ms (and
// signals asked), every later one with 202, and pushes with 202. events lists
// what it served, in order.
func throttledOnce(t *testing.T) (url string, asked <-chan struct{}, events func() string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	ask := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/notifications":
			seen = append(seen, "push")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{}`))
		case "/notifications/retract":
			if len(seen) == 0 {
				seen = append(seen, "retract 429")
				close(ask)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"status":429,"code":"rate_limit.exceeded","retry_after_ms":300}`))
				return
			}
			seen = append(seen, "retract")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"devices":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, ask, func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(seen, ", ")
	}
}

// A queued retract that never got through leaves the id open, so the late
// resolve of that alert tries again instead of doing nothing.
func TestLateResolveRetriesAQueuedRetractThatFailed(t *testing.T) {
	retractsOf := func(t *testing.T, m mockServer, b *Bridge, alertname string) int {
		t.Helper()
		bodies, _ := m.retracts(t, b)
		n := 0
		for _, body := range bodies {
			if body["collapse_id"] == alertCollapseID(alertname) {
				n++
			}
		}
		return n
	}
	end := func(b *Bridge) {
		b.active["HighCPU"] = trackedHighCPU("fp1")
		b.Forget(context.Background(), makeSlug("HighCPU"))
	}
	lateResolve := func(b *Bridge) {
		b.handleResolved(context.Background(), resolvedAlert(), nil)
	}

	t.Run("refused", func(t *testing.T) {
		m := newMockServer(t, testutil.MockOptions{RetractStatus: http.StatusForbidden})
		b := newTestBridge(t, m.url, nil, retractConfig())
		end(b)
		if n := retractsOf(t, m, b, "HighCPU"); n != 1 {
			t.Fatalf("End made %d retracts, want 1", n)
		}
		lateResolve(b)
		if n := retractsOf(t, m, b, "HighCPU"); n != 2 {
			t.Errorf("got %d retracts in all, want the late resolve to try again", n)
		}
	})

	t.Run("dropped from a full queue", func(t *testing.T) {
		m := newMockServer(t, testutil.MockOptions{})
		b := newTestBridge(t, m.url, nil, retractConfig())
		for i := range retractQueueCap {
			endAndQueue(b, fmt.Sprint("Alert", i))
		}
		end(b)
		if n := retractsOf(t, m, b, "HighCPU"); n != 0 {
			t.Fatalf("got %d retracts from a full queue, want none", n)
		}
		lateResolve(b)
		if n := retractsOf(t, m, b, "HighCPU"); n != 1 {
			t.Errorf("got %d retracts, want the late resolve to make one", n)
		}
	})

	t.Run("reached no device", func(t *testing.T) {
		none := 0
		m := newMockServer(t, testutil.MockOptions{RetractDevices: &none})
		b := newTestBridge(t, m.url, nil, retractConfig())
		end(b)
		retractsOf(t, m, b, "HighCPU")
		lateResolve(b)
		if n := retractsOf(t, m, b, "HighCPU"); n != 1 {
			t.Errorf("got %d retracts, want 1: the first one went through", n)
		}
	})
}

// A retract queued while the plugin stops, or still queued when it stopped,
// is dropped and shows in the delivery log.
func TestRetractsDroppedAtStopAreRecorded(t *testing.T) {
	asked := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case asked <- struct{}{}:
		default:
		}
		w.Header().Set("Retry-After", "60")
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status":429,"code":"rate_limit.exceeded"}`))
	}))
	t.Cleanup(srv.Close)
	cfg := retractConfig()
	cfg.Ack = false
	dl := &recLog{}
	pw := pushward.NewClient(srv.URL, "hlk_x")
	rc := pushward.NewClient(srv.URL, "hlk_x", pushward.WithRetryBudget(time.Nanosecond))
	b := NewBridge(pw, rc, nopQuerier{}, nil, NewPoller(nopQuerier{}, pw, time.Hour), dl, nil, cfg)
	t.Cleanup(b.Stop)

	endAndQueue(b, "Busy") // the worker waits out its Retry-After
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued retract was never made")
	}
	endAndQueue(b, "Queued")
	b.Stop()
	endAndQueue(b, "Late")

	for _, name := range []string{"Queued", "Late"} {
		found := false
		dl.mu.Lock()
		for _, e := range dl.entries {
			if e.slug == makeSlug(name) && e.action == "retract" && !e.ok && e.detail == "plugin stopping" {
				found = true
			}
		}
		dl.mu.Unlock()
		if !found {
			t.Errorf("no dropped retract recorded for %s", name)
		}
	}
	if n := len(b.retracts); n != 0 {
		t.Errorf("%d retracts left in the queue after Stop, want none", n)
	}
}
