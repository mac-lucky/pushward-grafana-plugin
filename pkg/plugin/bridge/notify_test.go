package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mac-lucky/pushward-integrations/shared/e2e"
	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	"github.com/mac-lucky/pushward-integrations/shared/syncx"
	"github.com/mac-lucky/pushward-integrations/shared/testutil"
	"github.com/mac-lucky/pushward-integrations/shared/text"
)

// recordedReq is one request the stub PushWard server received.
type recordedReq struct {
	method string
	path   string
	body   map[string]any
}

// stubServer is an httptest server standing in for api.pushward.app. It records
// every request (method, path, decoded JSON body) and answers 200 so the client
// treats each call as success without retrying.
type stubServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recordedReq
}

func newStubServer(t *testing.T) *stubServer {
	return newStubServerFunc(t, func(string) int { return http.StatusOK })
}

// newStubServerFunc lets a test choose the response status per request path
// (e.g. fail /notifications while /activities succeeds).
func newStubServerFunc(t *testing.T, status func(path string) int) *stubServer {
	t.Helper()
	s := &stubServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recordedReq{method: r.Method, path: r.URL.Path, body: body})
		s.mu.Unlock()
		w.WriteHeader(status(r.URL.Path))
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	return s
}

// find returns the first recorded request matching method+path, or nil.
func (s *stubServer) find(method, path string) *recordedReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.reqs {
		if s.reqs[i].method == method && s.reqs[i].path == path {
			return &s.reqs[i]
		}
	}
	return nil
}

// newTestBridge builds a bridge whose PushWard client points at url and whose
// Grafana resolver is resolver - nil for the tests that never look one up, which
// is also the shape a Grafana instance without a configured datasource produces.
// Metrics and the delivery log are nil too (all three are nil-safe); the poller
// is real but backed by a no-op querier so no metrics are queried.
func newTestBridge(t *testing.T, url string, resolver GrafanaResolver, cfg Config) *Bridge {
	t.Helper()
	pw := pushward.NewClient(url, "hlk_x")
	poller := NewPoller(nopQuerier{}, pw, time.Hour)
	t.Cleanup(func() {
		poller.StopAll()
		poller.Wait()
	})
	return &Bridge{
		pwClient:      pw,
		poller:        poller,
		grafanaClient: resolver,
		active:        make(map[string]*alertState),
		capDrops:      syncx.NewDropCounter(100),
		cfg:           cfg,
	}
}

func firingAlert() alert {
	return alert{
		Status:      alertStatusFiring,
		Fingerprint: "fp1",
		Labels:      map[string]string{"alertname": "HighCPU", "severity": "critical", "instance": "node-1"},
		Annotations: map[string]string{"summary": "CPU is high"},
	}
}

func TestAlsoNotifyFiringSendsActiveNotification(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, Config{AlsoNotify: true, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.handleFiring(context.Background(), firingAlert())

	if s.find(http.MethodPost, "/activities") == nil {
		t.Error("expected a POST /activities for the Live Activity")
	}
	notif := s.find(http.MethodPost, "/notifications")
	if notif == nil {
		t.Fatal("expected a POST /notifications when AlsoNotify is on, got none")
	}
	if got := notif.body["title"]; got != "HighCPU" {
		t.Errorf("notification title = %v, want HighCPU", got)
	}
	if got := notif.body["level"]; got != pushward.LevelActive {
		t.Errorf("notification level = %v, want %q", got, pushward.LevelActive)
	}
	if got, _ := notif.body["body"].(string); got != "CPU is high" {
		t.Errorf("notification body = %q, want %q", got, "CPU is high")
	}
	if sub, _ := notif.body["subtitle"].(string); !strings.HasPrefix(sub, "Grafana") {
		t.Errorf("notification subtitle = %q, want it to start with Grafana", sub)
	}
	// Without this the companion push does not deep-link into the timeline
	// activity it is about.
	create := s.find(http.MethodPost, "/activities")
	wantSlug, _ := create.body["slug"].(string)
	if got, _ := notif.body["activity_slug"].(string); got == "" || got != wantSlug {
		t.Errorf("notification activity_slug = %q, want the activity's slug %q", got, wantSlug)
	}
}

func TestAlsoNotifyFiringUsesConfiguredLevel(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, Config{AlsoNotify: true, NotifyLevel: pushward.LevelCritical, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.handleFiring(context.Background(), firingAlert())

	notif := s.find(http.MethodPost, "/notifications")
	if notif == nil {
		t.Fatal("expected a POST /notifications when AlsoNotify is on, got none")
	}
	if got := notif.body["level"]; got != pushward.LevelCritical {
		t.Errorf("notification level = %v, want %q", got, pushward.LevelCritical)
	}
}

func TestAlsoNotifyOffSendsNoNotification(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, Config{AlsoNotify: false, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.handleFiring(context.Background(), firingAlert())

	if s.find(http.MethodPost, "/activities") == nil {
		t.Error("expected a POST /activities even with AlsoNotify off")
	}
	if notif := s.find(http.MethodPost, "/notifications"); notif != nil {
		t.Errorf("expected no /notifications when AlsoNotify is off, got %+v", notif.body)
	}
}

func TestAlsoNotifyResolvedCarriesConfiguredLevel(t *testing.T) {
	cases := []struct {
		name      string
		cfgLevel  string
		wantLevel string
	}{
		{name: "default config resolves at active", cfgLevel: "", wantLevel: pushward.LevelActive},
		{name: "silent config resolves at passive", cfgLevel: pushward.LevelPassive, wantLevel: pushward.LevelPassive},
		{name: "critical config resolves at critical", cfgLevel: pushward.LevelCritical, wantLevel: pushward.LevelCritical},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubServer(t)
			b := newTestBridge(t, s.URL, nil, Config{AlsoNotify: true, NotifyLevel: tc.cfgLevel, SeverityLabel: "severity", DefaultSeverity: "warning"})

			// Seed the alert as already tracked so handleResolved ends it.
			const mapKey = "HighCPU"
			b.active[mapKey] = &alertState{
				slug:         makeSlug(mapKey),
				alertname:    "HighCPU",
				fingerprints: map[string]struct{}{"fp1": {}},
				lastSeen:     time.Now(),
			}

			b.handleResolved(context.Background(), alert{
				Status:      alertStatusResolved,
				Fingerprint: "fp1",
				Labels:      map[string]string{"alertname": "HighCPU"},
				Annotations: map[string]string{"summary": "CPU back to normal"},
			})

			notif := s.find(http.MethodPost, "/notifications")
			if notif == nil {
				t.Fatal("expected a POST /notifications on resolve when AlsoNotify is on, got none")
			}
			if got := notif.body["level"]; got != tc.wantLevel {
				t.Errorf("notification level = %v, want %q", got, tc.wantLevel)
			}
			body, _ := notif.body["body"].(string)
			if !strings.HasPrefix(body, "Resolved") || !strings.Contains(body, "CPU back to normal") {
				t.Errorf("notification body = %q, want it to start with Resolved and include the summary", body)
			}
		})
	}
}

// TestBuildAlertNotification exercises the pure builder directly: field values,
// the empty-summary body fallback (the server rejects an empty body), the
// stable collapse id (so a resolved push replaces the firing one), and the
// length caps.
func TestBuildAlertNotification(t *testing.T) {
	cases := []struct {
		name      string
		a         alert
		alertname string
		resolved  bool
		level     string
		wantLevel string
		wantBody  string
		wantSub   string
	}{
		{
			name:      "firing with summary (normal)",
			a:         alert{Labels: map[string]string{"instance": "node-1"}, Annotations: map[string]string{"summary": "CPU is high"}},
			alertname: "HighCPU", resolved: false, level: pushward.LevelActive,
			wantLevel: pushward.LevelActive, wantBody: "CPU is high", wantSub: "Grafana · node-1",
		},
		{
			name:      "firing without summary falls back to alertname (empty level defaults active)",
			a:         alert{Labels: map[string]string{}},
			alertname: "HighCPU", resolved: false, level: "",
			wantLevel: pushward.LevelActive, wantBody: "HighCPU", wantSub: "Grafana",
		},
		{
			name:      "firing silent uses passive",
			a:         alert{Annotations: map[string]string{"summary": "CPU is high"}},
			alertname: "HighCPU", resolved: false, level: pushward.LevelPassive,
			wantLevel: pushward.LevelPassive, wantBody: "CPU is high", wantSub: "Grafana",
		},
		{
			name:      "resolved carries the configured level (normal)",
			a:         alert{Annotations: map[string]string{"summary": "CPU back to normal"}},
			alertname: "HighCPU", resolved: true, level: pushward.LevelActive,
			wantLevel: pushward.LevelActive, wantBody: "Resolved · CPU back to normal", wantSub: "Grafana",
		},
		{
			name:      "resolved silent uses passive",
			a:         alert{Annotations: map[string]string{"summary": "CPU back to normal"}},
			alertname: "HighCPU", resolved: true, level: pushward.LevelPassive,
			wantLevel: pushward.LevelPassive, wantBody: "Resolved · CPU back to normal", wantSub: "Grafana",
		},
		{
			name:      "resolved without summary (backstop empty alert)",
			a:         alert{},
			alertname: "HighCPU", resolved: true, level: pushward.LevelActive,
			wantLevel: pushward.LevelActive, wantBody: "Resolved", wantSub: "Grafana",
		},
		{
			name:      "anonymous alert critical uses fallback title as body",
			a:         alert{},
			alertname: "Grafana Alert", resolved: false, level: pushward.LevelCritical,
			wantLevel: pushward.LevelCritical, wantBody: "Grafana Alert", wantSub: "Grafana",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := buildAlertNotification(tc.a, "grafana-test", tc.alertname, tc.resolved, tc.level)
			if req.Title != tc.alertname {
				t.Errorf("Title = %q, want %q", req.Title, tc.alertname)
			}
			if req.Level != tc.wantLevel {
				t.Errorf("Level = %q, want %q", req.Level, tc.wantLevel)
			}
			if req.Body != tc.wantBody {
				t.Errorf("Body = %q, want %q", req.Body, tc.wantBody)
			}
			if req.Body == "" {
				t.Error("Body must never be empty (server enforces minLength 1)")
			}
			if req.Subtitle != tc.wantSub {
				t.Errorf("Subtitle = %q, want %q", req.Subtitle, tc.wantSub)
			}
			if req.ThreadID != "grafana" || req.Source != "grafana" {
				t.Errorf("ThreadID/Source = %q/%q, want grafana/grafana", req.ThreadID, req.Source)
			}
			if req.Push == nil || !*req.Push {
				t.Error("Push must be explicitly true so the notification actually alerts")
			}
		})
	}

	t.Run("collapse id is stable across firing and resolved", func(t *testing.T) {
		a := alert{Annotations: map[string]string{"summary": "x"}}
		firing := buildAlertNotification(a, "grafana-test", "HighCPU", false, pushward.LevelActive)
		resolved := buildAlertNotification(a, "grafana-test", "HighCPU", true, pushward.LevelActive)
		if firing.CollapseID != resolved.CollapseID {
			t.Errorf("collapse ids differ (%q vs %q): the resolved push would stack instead of replacing the firing one",
				firing.CollapseID, resolved.CollapseID)
		}
		if want := text.SlugHash("grafana", "HighCPU", 6); firing.CollapseID != want {
			t.Errorf("CollapseID = %q, want %q", firing.CollapseID, want)
		}
	})

	t.Run("oversized fields are capped", func(t *testing.T) {
		a := alert{
			Labels:      map[string]string{"instance": strings.Repeat("y", 200)},
			Annotations: map[string]string{"summary": strings.Repeat("x", 500)},
		}
		req := buildAlertNotification(a, "grafana-test", "HighCPU", false, pushward.LevelActive)
		if n := utf8.RuneCountInString(req.Subtitle); n > maxNotifySubtitleRunes {
			t.Errorf("Subtitle rune count = %d, want <= %d", n, maxNotifySubtitleRunes)
		}
		if n := utf8.RuneCountInString(req.Body); n > maxNotifyBodyRunes {
			t.Errorf("Body rune count = %d, want <= %d", n, maxNotifyBodyRunes)
		}
	})
}

// TestAlsoNotifyFiringOnlyOncePerAlert verifies a re-fire of an already-tracked
// alert does not send another active push (the isNew gate).
func TestAlsoNotifyFiringOnlyOncePerAlert(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, Config{AlsoNotify: true, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.active["HighCPU"] = &alertState{
		slug:         makeSlug("HighCPU"),
		alertname:    "HighCPU",
		fingerprints: map[string]struct{}{"fp0": {}},
		lastSeen:     time.Now(),
	}

	b.handleFiring(context.Background(), firingAlert())

	if notif := s.find(http.MethodPost, "/notifications"); notif != nil {
		t.Errorf("a re-fire must not send another active push, got %+v", notif.body)
	}
}

// TestAlsoNotifyFailureDoesNotDarkTimeline verifies that when the notification
// POST fails, the core timeline UpdateActivity still happens (the notification
// is best-effort and now deferred after the timeline work).
func TestAlsoNotifyFailureDoesNotDarkTimeline(t *testing.T) {
	s := newStubServerFunc(t, func(path string) int {
		if path == "/notifications" {
			return http.StatusUnprocessableEntity // 4xx: fails fast, no retry storm
		}
		return http.StatusOK
	})
	b := newTestBridge(t, s.URL, nil, Config{AlsoNotify: true, SeverityLabel: "severity", DefaultSeverity: "warning"})

	a := firingAlert()
	a.Values = map[string]float64{"A": 42} // gives the firing path a value so a timeline update is attempted

	b.handleFiring(context.Background(), a)

	if s.find(http.MethodPatch, "/activities/"+makeSlug("HighCPU")) == nil {
		t.Error("timeline UpdateActivity must still happen even though the notification POST failed")
	}
	if s.find(http.MethodPost, "/notifications") == nil {
		t.Error("the notification should still have been attempted")
	}
}

// TestAlsoNotifyPartialResolutionNoPush verifies that resolving one of several
// firing instances does not end the activity or send a resolved push.
func TestAlsoNotifyPartialResolutionNoPush(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, Config{AlsoNotify: true, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.active["HighCPU"] = &alertState{
		slug:         makeSlug("HighCPU"),
		alertname:    "HighCPU",
		fingerprints: map[string]struct{}{"fp1": {}, "fp2": {}},
		lastSeen:     time.Now(),
	}

	b.handleResolved(context.Background(), alert{
		Status:      alertStatusResolved,
		Fingerprint: "fp1",
		Labels:      map[string]string{"alertname": "HighCPU"},
	})

	if notif := s.find(http.MethodPost, "/notifications"); notif != nil {
		t.Errorf("resolving one of two instances must not send a resolved push, got %+v", notif.body)
	}
	if b.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1 (the alert is still firing)", b.ActiveCount())
	}
}

// TestAlsoNotifyResolvedOffSendsNoNotification verifies the AlsoNotify gate on
// the resolve path: the activity still ends, but no notification is sent.
func TestAlsoNotifyResolvedOffSendsNoNotification(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, Config{AlsoNotify: false, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.active["HighCPU"] = &alertState{
		slug:         makeSlug("HighCPU"),
		alertname:    "HighCPU",
		fingerprints: map[string]struct{}{"fp1": {}},
		lastSeen:     time.Now(),
	}

	b.handleResolved(context.Background(), alert{
		Status:      alertStatusResolved,
		Fingerprint: "fp1",
		Labels:      map[string]string{"alertname": "HighCPU"},
	})

	if s.find(http.MethodPatch, "/activities/"+makeSlug("HighCPU")) == nil {
		t.Error("the activity should still be ended even with AlsoNotify off")
	}
	if notif := s.find(http.MethodPost, "/notifications"); notif != nil {
		t.Errorf("no notification should be sent when AlsoNotify is off, got %+v", notif.body)
	}
}

// testE2EKey is a fixed encryption key for the sealing tests.
const testE2EKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func mustKey(t *testing.T) *e2e.Key {
	t.Helper()
	k, err := e2e.ParseKey(testE2EKey)
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	return k
}

// recLog is a DeliveryLogger that keeps every entry.
type recLog struct {
	mu      sync.Mutex
	entries []logEntry
}

type logEntry struct {
	slug, action string
	ok           bool
	detail       string
}

func (l *recLog) Log(_, slug, action string, ok bool, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, logEntry{slug: slug, action: action, ok: ok, detail: detail})
}

func (l *recLog) find(action string) *logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.entries {
		if l.entries[i].action == action {
			return &l.entries[i]
		}
	}
	return nil
}

// mockServer is the shared contract mock: it validates every request the way
// the server does and keeps acknowledged sends as receipts.
type mockServer struct {
	url   string
	calls *[]testutil.APICall
	mu    *sync.Mutex
}

func newMockServer(t *testing.T, opts testutil.MockOptions) mockServer {
	srv, calls, mu := testutil.MockPushWardServerWith(t, opts)
	return mockServer{url: srv.URL, calls: calls, mu: mu}
}

// requests returns the recorded calls to method+path, in order, with each
// call's index among all calls so a test can check what came first.
func (m mockServer) requests(t *testing.T, method, path string) (bodies []map[string]any, idx []int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range *m.calls {
		if c.Method != method || c.Path != path {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(c.Body, &body); err != nil {
			t.Fatalf("decode %s %s body: %v", method, path, err)
		}
		bodies = append(bodies, body)
		idx = append(idx, i)
	}
	return bodies, idx
}

func (m mockServer) rawNotifications(t *testing.T) []string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range *m.calls {
		if c.Method == http.MethodPost && c.Path == "/notifications" {
			out = append(out, string(c.Body))
		}
	}
	return out
}

func ackConfig() Config {
	return Config{AlsoNotify: true, Ack: true, AckRepeat: 120, AckExpire: 1800, SeverityLabel: "severity", DefaultSeverity: "warning"}
}

func resolvedAlert() alert {
	return alert{
		Status:      alertStatusResolved,
		Fingerprint: "fp1",
		Labels:      map[string]string{"alertname": "HighCPU"},
		Annotations: map[string]string{"summary": "CPU back to normal"},
	}
}

func TestAckFiringRepeatsAndResolveCancelsFirst(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, ackConfig())
	dl := &recLog{}
	b.deliveryLog = dl
	slug := makeSlug("HighCPU")

	b.handleFiring(context.Background(), firingAlert())

	notifs, _ := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 1 {
		t.Fatalf("got %d notifications after firing, want 1", len(notifs))
	}
	ack, _ := notifs[0]["acknowledge"].(map[string]any)
	if ack == nil {
		t.Fatalf("firing push has no acknowledge: %v", notifs[0])
	}
	if ack["repeat_seconds"] != float64(120) || ack["expire_seconds"] != float64(1800) {
		t.Errorf("acknowledge = %v, want repeat 120 / expire 1800", ack)
	}
	if tags, _ := notifs[0]["tags"].([]any); len(tags) != 1 || tags[0] != slug {
		t.Errorf("tags = %v, want [%s]", notifs[0]["tags"], slug)
	}
	if got := notifs[0]["collapse_id"]; got != text.SlugHash("grafana", "HighCPU", 6) {
		t.Errorf("collapse_id = %v, want it kept on an acknowledged push", got)
	}
	if e := dl.find("notified"); e == nil || !strings.Contains(e.detail, "ack") {
		t.Errorf("delivery log notified = %+v, want detail noting ack", e)
	}

	b.handleResolved(context.Background(), resolvedAlert())

	cancels, cancelIdx := m.requests(t, http.MethodPost, "/notifications/receipts/cancel")
	if len(cancels) != 1 || cancels[0]["tag"] != slug {
		t.Fatalf("cancel requests = %v, want one for tag %s", cancels, slug)
	}
	notifs, notifIdx := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 2 {
		t.Fatalf("got %d notifications, want firing + resolved", len(notifs))
	}
	if cancelIdx[0] > notifIdx[1] {
		t.Error("repeats were canceled after the resolved push; a late repeat would replace it")
	}
	if _, ok := notifs[1]["acknowledge"]; ok {
		t.Error("the resolved push must not ask for acknowledge")
	}
	if _, ok := notifs[1]["tags"]; ok {
		t.Error("the resolved push must not carry tags")
	}
	if e := dl.find("ack-canceled"); e == nil || !e.ok || e.detail != "1" {
		t.Errorf("delivery log ack-canceled = %+v, want ok with count 1", e)
	}
}

func TestAckOffAtPassiveLevel(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	cfg := ackConfig()
	cfg.NotifyLevel = pushward.LevelPassive
	b := newTestBridge(t, m.url, nil, cfg)

	b.handleFiring(context.Background(), firingAlert())
	b.handleResolved(context.Background(), resolvedAlert())

	notifs, _ := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 2 {
		t.Fatalf("got %d notifications, want firing + resolved", len(notifs))
	}
	for _, n := range notifs {
		if _, ok := n["acknowledge"]; ok {
			t.Errorf("passive push asked for acknowledge, which the server refuses: %v", n)
		}
	}
	if cancels, _ := m.requests(t, http.MethodPost, "/notifications/receipts/cancel"); len(cancels) != 0 {
		t.Errorf("got %d cancel requests with acknowledge off, want none", len(cancels))
	}
}

func TestAckRefusedResendsWithoutAck(t *testing.T) {
	cases := []struct {
		status int
		code   string
		reason string
	}{
		{http.StatusConflict, pushward.ErrCodeNotificationReceiptLimit, pushward.ErrCodeNotificationReceiptLimit},
		{http.StatusUnprocessableEntity, pushward.ErrCodeNotificationReceiptDisabled, pushward.ErrCodeNotificationReceiptDisabled},
		{http.StatusUnprocessableEntity, pushward.ErrCodeNotificationAnswerURLUnavailable, pushward.ErrCodeNotificationAnswerURLUnavailable},
		{http.StatusUnprocessableEntity, pushward.ErrCodeNotificationEncryptedTooLarge, pushward.ErrCodeNotificationEncryptedTooLarge},
		{http.StatusBadRequest, pushward.ErrCodeNotificationInvalid, pushward.ErrCodeNotificationInvalid},
		{http.StatusUnprocessableEntity, "", "status 422"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{AckStatus: tc.status, AckCode: tc.code})
			b := newTestBridge(t, m.url, nil, ackConfig())
			dl := &recLog{}
			b.deliveryLog = dl
			slug := makeSlug("HighCPU")

			b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false)

			notifs, _ := m.requests(t, http.MethodPost, "/notifications")
			if len(notifs) != 2 {
				t.Fatalf("got %d sends, want the refused one plus one without acknowledge", len(notifs))
			}
			if _, ok := notifs[0]["acknowledge"]; !ok {
				t.Error("first send should carry acknowledge")
			}
			if _, ok := notifs[1]["acknowledge"]; ok {
				t.Error("the resend must drop acknowledge")
			}
			if _, ok := notifs[1]["tags"]; ok {
				t.Error("the resend must drop the tags, which need acknowledge")
			}
			if notifs[1]["title"] != "HighCPU" || notifs[1]["collapse_id"] != notifs[0]["collapse_id"] {
				t.Errorf("resend changed the alert: %v", notifs[1])
			}
			e := dl.find("notified")
			if e == nil || !e.ok || !strings.Contains(e.detail, "ack refused: "+tc.reason) {
				t.Errorf("delivery log notified = %+v, want ok with ack refused: %s", e, tc.reason)
			}
		})
	}
}

func TestAckRefusalOnlyForAckErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"receipt cap", &pushward.HTTPError{StatusCode: 409, Code: pushward.ErrCodeNotificationReceiptLimit}, pushward.ErrCodeNotificationReceiptLimit},
		{"receipts disabled", &pushward.HTTPError{StatusCode: 422, Code: pushward.ErrCodeNotificationReceiptDisabled}, pushward.ErrCodeNotificationReceiptDisabled},
		{"schema", &pushward.HTTPError{StatusCode: 422}, "status 422"},
		{"handler rule", &pushward.HTTPError{StatusCode: 400, Code: pushward.ErrCodeNotificationInvalid}, pushward.ErrCodeNotificationInvalid},
		{"other 409", &pushward.HTTPError{StatusCode: 409, Code: "activity.limit_exceeded"}, ""},
		{"org key", &pushward.HTTPError{StatusCode: 422, Code: pushward.ErrCodeNotificationEncryptionUnavailable}, ""},
		{"bad key", &pushward.HTTPError{StatusCode: 401}, ""},
		{"forbidden", &pushward.HTTPError{StatusCode: 403}, ""},
		{"quota", &pushward.QuotaExceededError{HTTPError: &pushward.HTTPError{StatusCode: 429, Code: pushward.ErrCodeQuotaExceeded}}, ""},
		{"server error", fmt.Errorf("max retries exceeded: %w", &pushward.HTTPError{StatusCode: 503}), ""},
		{"network", errors.New("dial tcp: connection refused"), ""},
		{"success", nil, ""},
	}
	for _, tc := range cases {
		if got := ackRefusal(tc.err); got != tc.want {
			t.Errorf("%s: ackRefusal = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAckNoResendWhenKeyRejected(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{NotifyStatus: status})
			b := newTestBridge(t, m.url, nil, ackConfig())
			slug := makeSlug("HighCPU")

			b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false)

			if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != 1 {
				t.Errorf("got %d sends after a %d, want 1 (no resend)", len(notifs), status)
			}
		})
	}
}

// TestAckCanceledOnEveryEnd covers the paths that end an alert without a
// resolved webhook for a tracked entry: each one stops the repeats.
func TestAckCanceledOnEveryEnd(t *testing.T) {
	slug := makeSlug("HighCPU")
	cases := []struct {
		name         string
		end          func(b *Bridge)
		wantResolved bool
	}{
		{"alertmanager backstop", func(b *Bridge) {
			b.endAlertActivity(context.Background(), "HighCPU", &alertState{slug: slug, alertname: "HighCPU"})
		}, true},
		{"stale sweep", func(b *Bridge) {
			b.active["HighCPU"] = &alertState{slug: slug, alertname: "HighCPU", lastSeen: time.Now().Add(-time.Hour)}
			b.sweepStale(context.Background(), time.Minute)
		}, false},
		{"End from the Activities page", func(b *Bridge) {
			b.active["HighCPU"] = &alertState{slug: slug, alertname: "HighCPU", lastSeen: time.Now()}
			b.Forget(context.Background(), slug)
		}, false},
		{"resolved webhook for an untracked alert", func(b *Bridge) {
			b.handleResolved(context.Background(), resolvedAlert())
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, nil, ackConfig())
			dl := &recLog{}
			b.deliveryLog = dl
			// The firing push, so there is a receipt to cancel.
			b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false)

			tc.end(b)

			cancels, cancelIdx := m.requests(t, http.MethodPost, "/notifications/receipts/cancel")
			if len(cancels) != 1 || cancels[0]["tag"] != slug {
				t.Fatalf("cancel requests = %v, want one for tag %s", cancels, slug)
			}
			if e := dl.find("ack-canceled"); e == nil || e.detail != "1" {
				t.Errorf("delivery log ack-canceled = %+v, want count 1", e)
			}
			notifs, notifIdx := m.requests(t, http.MethodPost, "/notifications")
			if !tc.wantResolved {
				if len(notifs) != 1 {
					t.Errorf("got %d notifications, want only the firing one", len(notifs))
				}
				return
			}
			if len(notifs) != 2 {
				t.Fatalf("got %d notifications, want firing + resolved", len(notifs))
			}
			if cancelIdx[0] > notifIdx[1] {
				t.Error("repeats were canceled after the resolved push")
			}
		})
	}
}

func TestAckCancelFailureStillSendsResolved(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{CancelStatus: http.StatusBadRequest})
	b := newTestBridge(t, m.url, nil, ackConfig())
	dl := &recLog{}
	b.deliveryLog = dl
	b.active["HighCPU"] = &alertState{
		slug:         makeSlug("HighCPU"),
		alertname:    "HighCPU",
		fingerprints: map[string]struct{}{"fp1": {}},
		lastSeen:     time.Now(),
	}

	b.handleResolved(context.Background(), resolvedAlert())

	if e := dl.find("ack-cancel"); e == nil || e.ok {
		t.Errorf("delivery log ack-cancel = %+v, want a recorded failure", e)
	}
	notifs, _ := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 1 {
		t.Fatalf("got %d notifications, want the resolved push despite the failed cancel", len(notifs))
	}
	if body, _ := notifs[0]["body"].(string); !strings.HasPrefix(body, "Resolved") {
		t.Errorf("body = %q, want the resolved push", body)
	}
}

func TestSealedNotificationOpensWithKey(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	cfg := ackConfig()
	cfg.E2EKey = mustKey(t)
	b := newTestBridge(t, m.url, nil, cfg)
	dl := &recLog{}
	b.deliveryLog = dl
	slug := makeSlug("HighCPU")

	b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false)

	notifs, _ := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notifs))
	}
	n := notifs[0]
	for _, f := range []string{"title", "subtitle", "body", "url"} {
		if v, _ := n[f].(string); v != "" {
			t.Errorf("sealed push carries %s in the clear: %q", f, v)
		}
	}
	raw := m.rawNotifications(t)[0]
	for _, s := range []string{"HighCPU", "node-1", "CPU is high"} {
		if strings.Contains(raw, s) {
			t.Errorf("sealed request contains %q: %s", s, raw)
		}
	}
	env, _ := n["encrypted"].(string)
	msg, err := e2e.Open(mustKey(t), env)
	if err != nil {
		t.Fatalf("open envelope: %v", err)
	}
	if msg.Title != "HighCPU" || msg.Subtitle != "Grafana · node-1" || msg.Body != "CPU is high" {
		t.Errorf("opened %+v, want the alert text", msg)
	}
	// What the server needs to deliver stays readable.
	if n["level"] != pushward.LevelActive || n["collapse_id"] != text.SlugHash("grafana", "HighCPU", 6) || n["activity_slug"] != slug {
		t.Errorf("sealed push lost its delivery fields: %v", n)
	}
	if _, ok := n["acknowledge"]; !ok {
		t.Error("sealed push dropped acknowledge")
	}
	if e := dl.find("notified"); e == nil || !strings.Contains(e.detail, "encrypted") {
		t.Errorf("delivery log notified = %+v, want detail noting encrypted", e)
	}
}

func TestInvalidKeySendsContentFreeAlert(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		t.Run(fmt.Sprintf("resolved=%v", resolved), func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			cfg := ackConfig()
			cfg.NotifyLevel = pushward.LevelCritical
			cfg.E2EKeyError = "an encryption key is 64 hex characters, got 12"
			b := newTestBridge(t, m.url, nil, cfg)
			dl := &recLog{}
			b.deliveryLog = dl
			slug := makeSlug("HighCPU")
			a := firingAlert()
			if resolved {
				a = resolvedAlert()
				a.Labels["instance"] = "node-1"
			}

			b.sendAlertNotification(context.Background(), slog.Default(), a, slug, slug, "HighCPU", resolved)

			notifs, _ := m.requests(t, http.MethodPost, "/notifications")
			if len(notifs) != 1 {
				t.Fatalf("got %d notifications, want 1: an invalid key must not cost the alert", len(notifs))
			}
			n := notifs[0]
			wantTitle := contentFreeFiring
			if resolved {
				wantTitle = contentFreeResolved
			}
			if n["title"] != wantTitle || n["body"] != contentFreeBody {
				t.Errorf("title/body = %v / %v, want %q / %q", n["title"], n["body"], wantTitle, contentFreeBody)
			}
			if _, ok := n["subtitle"]; ok {
				t.Errorf("content-free push kept the subtitle %v", n["subtitle"])
			}
			if _, ok := n["encrypted"]; ok {
				t.Error("content-free push carries an envelope")
			}
			if n["level"] != pushward.LevelCritical {
				t.Errorf("level = %v, want the configured level", n["level"])
			}
			raw := m.rawNotifications(t)[0]
			for _, s := range []string{"HighCPU", "node-1", "CPU"} {
				if strings.Contains(raw, s) {
					t.Errorf("content-free request contains %q: %s", s, raw)
				}
			}
			if e := dl.find("notified"); e == nil || !strings.Contains(e.detail, "content-free") {
				t.Errorf("delivery log notified = %+v, want detail noting content-free", e)
			}
		})
	}
}

// resolvingResolver resolves the alert from inside the firing path, which is
// where a concurrent resolved webhook lands when it overtakes the deferred
// firing push.
type resolvingResolver struct {
	resolve func()
}

func (r resolvingResolver) ExtractRuleUID(string) string { return "rule-1" }

func (r resolvingResolver) GetRuleQuery(context.Context, string) (string, string, error) {
	r.resolve()
	return "", "", errors.New("no query")
}

func (r resolvingResolver) IsAlertFiring(context.Context, string) (bool, error) { return false, nil }

func TestAckCanceledWhenResolveOvertakesFiringPush(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	var b *Bridge
	res := resolvingResolver{resolve: func() { b.handleResolved(context.Background(), resolvedAlert()) }}
	b = newTestBridge(t, m.url, res, ackConfig())
	dl := &recLog{}
	b.deliveryLog = dl
	slug := makeSlug("HighCPU")

	a := firingAlert()
	a.GeneratorURL = "http://grafana/alerting/grafana/rule-1/view"
	b.handleFiring(context.Background(), a)

	notifs, notifIdx := m.requests(t, http.MethodPost, "/notifications")
	firing := -1
	for i, n := range notifs {
		if _, ok := n["acknowledge"]; ok {
			firing = notifIdx[i]
		}
	}
	if firing < 0 {
		t.Fatalf("no acknowledged firing push among %v", notifs)
	}
	cancels, cancelIdx := m.requests(t, http.MethodPost, "/notifications/receipts/cancel")
	if len(cancels) == 0 || cancelIdx[len(cancelIdx)-1] < firing {
		t.Fatalf("cancel requests at %v, want one after the acknowledged push at %d", cancelIdx, firing)
	}
	if cancels[len(cancels)-1]["tag"] != slug {
		t.Errorf("late cancel tag = %v, want %s", cancels[len(cancels)-1]["tag"], slug)
	}
	var stopped bool
	dl.mu.Lock()
	for _, e := range dl.entries {
		stopped = stopped || (e.action == "ack-canceled" && e.detail == "1")
	}
	dl.mu.Unlock()
	if !stopped {
		t.Error("the repeats of the late firing push were never stopped")
	}
}

// problemServer answers POST /notifications with whatever answer returns for
// the decoded body, as a Problem when the status is an error, and keeps every
// body it got. Everything else gets a 200.
func problemServer(t *testing.T, answer func(body map[string]any) (int, string)) (url string, sent func() []string) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/notifications" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		status, code := answer(body)
		if status >= 400 {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "detail": "refused"})
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id":1,"pushed":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func TestOrgKeySendsContentFreeAlert(t *testing.T) {
	// An organization key gets 422 for every encrypted send.
	orgKey := func(body map[string]any) (int, string) {
		if _, ok := body["encrypted"]; ok {
			return http.StatusUnprocessableEntity, pushward.ErrCodeNotificationEncryptionUnavailable
		}
		return http.StatusCreated, ""
	}
	for _, resolved := range []bool{false, true} {
		t.Run(fmt.Sprintf("resolved=%v", resolved), func(t *testing.T) {
			url, sent := problemServer(t, orgKey)
			cfg := ackConfig()
			cfg.E2EKey = mustKey(t)
			b := newTestBridge(t, url, nil, cfg)
			dl := &recLog{}
			b.deliveryLog = dl
			slug := makeSlug("HighCPU")
			a := firingAlert()
			if resolved {
				a = resolvedAlert()
			}

			b.sendAlertNotification(context.Background(), slog.Default(), a, slug, slug, "HighCPU", resolved)

			bodies := sent()
			if len(bodies) != 2 {
				t.Fatalf("got %d sends, want the encrypted one plus a content-free resend", len(bodies))
			}
			var second map[string]any
			if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
				t.Fatal(err)
			}
			wantTitle := contentFreeFiring
			if resolved {
				wantTitle = contentFreeResolved
			}
			if second["title"] != wantTitle || second["body"] != contentFreeOrgBody {
				t.Errorf("resend title/body = %v / %v, want %q / %q", second["title"], second["body"], wantTitle, contentFreeOrgBody)
			}
			if _, ok := second["encrypted"]; ok {
				t.Error("the resend still carries the envelope")
			}
			if _, ok := second["acknowledge"]; ok == resolved {
				t.Errorf("resend acknowledge present = %v, want %v (firing only)", ok, !resolved)
			}
			if tags, _ := second["tags"].([]any); !resolved && (len(tags) != 1 || tags[0] != slug) {
				t.Errorf("resend tags = %v, want [%s] so the resolve can still cancel it", second["tags"], slug)
			}
			for _, s := range []string{"HighCPU", "node-1", "CPU"} {
				if strings.Contains(bodies[1], s) {
					t.Errorf("content-free resend contains %q: %s", s, bodies[1])
				}
			}
			e := dl.find("notified")
			if e == nil || !e.ok || !strings.Contains(e.detail, "organization keys") {
				t.Errorf("delivery log notified = %+v, want ok noting organization keys", e)
			}
			if f := dl.find("notify"); f != nil {
				t.Errorf("delivery log recorded a failure %+v, want the alert delivered", f)
			}
		})
	}

	// Only the organization refusal earns the content-free resend; any other
	// failure of the sealed send is a failed notify, as before.
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{"server error", http.StatusInternalServerError, ""},
		{"other 422", http.StatusUnprocessableEntity, pushward.ErrCodeNotificationActivityNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, sent := problemServer(t, func(map[string]any) (int, string) { return tc.status, tc.code })
			cfg := ackConfig()
			cfg.E2EKey = mustKey(t)
			b := newTestBridge(t, url, nil, cfg)
			// No retries, so a 5xx is one request too.
			b.pwClient = pushward.NewClient(url, "hlk_x", pushward.WithRetryBudget(time.Nanosecond))
			dl := &recLog{}
			b.deliveryLog = dl
			slug := makeSlug("HighCPU")

			b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false)

			if bodies := sent(); len(bodies) != 1 {
				t.Fatalf("got %d sends, want only the sealed one: %v", len(bodies), bodies)
			}
			if e := dl.find("notify"); e == nil || e.ok {
				t.Errorf("delivery log notify = %+v, want a recorded failure", e)
			}
			if e := dl.find("notified"); e != nil {
				t.Errorf("delivery log notified = %+v, want none", e)
			}
		})
	}

	t.Run("content-free resend keeps the acknowledge rules", func(t *testing.T) {
		// Encrypted sends are refused for the organization, then the
		// acknowledged content-free one for the receipt cap.
		url, sent := problemServer(t, func(body map[string]any) (int, string) {
			if _, ok := body["encrypted"]; ok {
				return http.StatusUnprocessableEntity, pushward.ErrCodeNotificationEncryptionUnavailable
			}
			if _, ok := body["acknowledge"]; ok {
				return http.StatusConflict, pushward.ErrCodeNotificationReceiptLimit
			}
			return http.StatusCreated, ""
		})
		cfg := ackConfig()
		cfg.E2EKey = mustKey(t)
		b := newTestBridge(t, url, nil, cfg)
		dl := &recLog{}
		b.deliveryLog = dl
		slug := makeSlug("HighCPU")

		b.sendAlertNotification(context.Background(), slog.Default(), firingAlert(), slug, slug, "HighCPU", false)

		bodies := sent()
		if len(bodies) != 3 {
			t.Fatalf("got %d sends, want encrypted, content-free with ack, content-free without", len(bodies))
		}
		if strings.Contains(bodies[2], "acknowledge") || strings.Contains(bodies[2], "HighCPU") {
			t.Errorf("last send = %s, want content-free without acknowledge", bodies[2])
		}
		if e := dl.find("notified"); e == nil || !strings.Contains(e.detail, "ack refused") {
			t.Errorf("delivery log notified = %+v, want ack refused noted", e)
		}
	})
}
