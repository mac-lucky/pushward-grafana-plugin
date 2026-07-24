package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	"github.com/mac-lucky/pushward-integrations/shared/syncx"
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

// newTestBridge builds a bridge whose PushWard client points at url. Metrics,
// delivery log and Grafana resolver are nil (all nil-safe); the poller is real
// but backed by a no-op querier so no metrics are queried.
func newTestBridge(t *testing.T, url string, cfg Config) *Bridge {
	t.Helper()
	pw := pushward.NewClient(url, "hlk_x")
	poller := NewPoller(nopQuerier{}, pw, time.Hour)
	t.Cleanup(func() {
		poller.StopAll()
		poller.Wait()
	})
	return &Bridge{
		pwClient: pw,
		poller:   poller,
		active:   make(map[string]*alertState),
		capDrops: syncx.NewDropCounter(100),
		cfg:      cfg,
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
	b := newTestBridge(t, s.URL, Config{AlsoNotify: true, SeverityLabel: "severity", DefaultSeverity: "warning"})

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
}

func TestAlsoNotifyFiringUsesConfiguredLevel(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, Config{AlsoNotify: true, NotifyLevel: pushward.LevelCritical, SeverityLabel: "severity", DefaultSeverity: "warning"})

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
	b := newTestBridge(t, s.URL, Config{AlsoNotify: false, SeverityLabel: "severity", DefaultSeverity: "warning"})

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
			b := newTestBridge(t, s.URL, Config{AlsoNotify: true, NotifyLevel: tc.cfgLevel, SeverityLabel: "severity", DefaultSeverity: "warning"})

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
			req := buildAlertNotification(tc.a, tc.alertname, tc.resolved, tc.level)
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
			if !req.Push {
				t.Error("Push must be true so the notification actually alerts")
			}
		})
	}

	t.Run("collapse id is stable across firing and resolved", func(t *testing.T) {
		a := alert{Annotations: map[string]string{"summary": "x"}}
		firing := buildAlertNotification(a, "HighCPU", false, pushward.LevelActive)
		resolved := buildAlertNotification(a, "HighCPU", true, pushward.LevelActive)
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
		req := buildAlertNotification(a, "HighCPU", false, pushward.LevelActive)
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
	b := newTestBridge(t, s.URL, Config{AlsoNotify: true, SeverityLabel: "severity", DefaultSeverity: "warning"})

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
	b := newTestBridge(t, s.URL, Config{AlsoNotify: true, SeverityLabel: "severity", DefaultSeverity: "warning"})

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
	b := newTestBridge(t, s.URL, Config{AlsoNotify: true, SeverityLabel: "severity", DefaultSeverity: "warning"})

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
	b := newTestBridge(t, s.URL, Config{AlsoNotify: false, SeverityLabel: "severity", DefaultSeverity: "warning"})

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
