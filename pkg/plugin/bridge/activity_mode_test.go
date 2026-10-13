package bridge

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/mac-lucky/pushward-integrations/shared/testutil"
)

// activityCalls counts the requests that create or change a Live Activity.
func (m mockServer) activityCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range *m.calls {
		if strings.HasPrefix(c.Path, "/activities") {
			n++
		}
	}
	return n
}

func labeledFiring(value string) alert {
	a := firingAlert()
	a.Labels[LabelActivity] = value
	return a
}

func TestWantsActivity(t *testing.T) {
	cases := []struct {
		mode, label string
		want        bool
	}{
		{"", "", true},
		{ActivityModeAll, "", true},
		{ActivityModeAll, "true", true},
		{ActivityModeAll, "false", false},
		{ActivityModeAll, "FALSE", false},
		{ActivityModeAll, "no", true},
		{ActivityModeLabeled, "", false},
		{ActivityModeLabeled, "true", true},
		{ActivityModeLabeled, " True ", true},
		{ActivityModeLabeled, "false", false},
		{ActivityModeLabeled, "yes", false},
	}
	for _, tc := range cases {
		b := &Bridge{cfg: Config{ActivityMode: tc.mode}}
		a := firingAlert()
		if tc.label != "" {
			a.Labels[LabelActivity] = tc.label
		}
		if got := b.wantsActivity(a); got != tc.want {
			t.Errorf("mode %q, label %q: wantsActivity = %v, want %v", tc.mode, tc.label, got, tc.want)
		}
	}
}

func TestActivityModeAllCreatesActivityForUnlabeledAlert(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, Config{SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.handleFiring(context.Background(), firingAlert(), nil)

	if creates, _ := m.requests(t, http.MethodPost, "/activities"); len(creates) != 1 {
		t.Fatalf("got %d activity creates, want 1", len(creates))
	}
	if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != 0 {
		t.Errorf("got %d notifications with AlsoNotify off, want none", len(notifs))
	}
	if got := b.ActiveAlerts(); len(got) != 1 {
		t.Errorf("ActiveAlerts = %v, want the activity listed", got)
	}
}

// An alert left without a Live Activity is pushed once when it fires and once
// when it resolves, with AlsoNotify off, and never touches /activities.
func TestNotifyOnlyAlertPushesFiringAndResolved(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		alert alert
	}{
		{"all mode, labeled false", ActivityModeAll, labeledFiring("false")},
		{"labeled mode, no label", ActivityModeLabeled, firingAlert()},
		{"labeled mode, other value", ActivityModeLabeled, labeledFiring("yes")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, nil, Config{ActivityMode: tc.mode, SeverityLabel: "severity", DefaultSeverity: "warning"})
			dl := &recLog{}
			b.deliveryLog = dl

			b.handleFiring(context.Background(), tc.alert, nil)
			// A repeat of the same alert must not push again.
			b.handleFiring(context.Background(), tc.alert, nil)

			notifs, _ := m.requests(t, http.MethodPost, "/notifications")
			if len(notifs) != 1 {
				t.Fatalf("got %d notifications after firing twice, want 1", len(notifs))
			}
			if got := notifs[0]["title"]; got != "HighCPU" {
				t.Errorf("title = %v, want HighCPU", got)
			}
			if got, _ := notifs[0]["body"].(string); got != "CPU is high" {
				t.Errorf("body = %q, want the summary", got)
			}
			if _, ok := notifs[0]["activity_slug"]; ok {
				t.Errorf("activity_slug = %v, want none: there is no activity to open", notifs[0]["activity_slug"])
			}
			if got := b.ActiveAlerts(); len(got) != 0 {
				t.Errorf("ActiveAlerts = %v, want no activity listed", got)
			}
			if e := dl.find("notified"); e == nil || !e.ok {
				t.Errorf("delivery log notified = %+v, want ok", e)
			}

			b.handleResolved(context.Background(), resolvedAlert())

			notifs, _ = m.requests(t, http.MethodPost, "/notifications")
			if len(notifs) != 2 {
				t.Fatalf("got %d notifications, want firing + resolved", len(notifs))
			}
			if got, _ := notifs[1]["body"].(string); !strings.HasPrefix(got, "Resolved") {
				t.Errorf("resolved body = %q, want it to start with Resolved", got)
			}
			if n := m.activityCalls(); n != 0 {
				t.Errorf("got %d /activities calls, want none", n)
			}
			if n := b.ActiveCount(); n != 0 {
				t.Errorf("ActiveCount = %d after resolve, want 0", n)
			}
		})
	}
}

func TestLabeledModeCreatesActivityForLabeledAlert(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, Config{ActivityMode: ActivityModeLabeled, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.handleFiring(context.Background(), labeledFiring("True"), nil)

	if creates, _ := m.requests(t, http.MethodPost, "/activities"); len(creates) != 1 {
		t.Fatalf("got %d activity creates, want 1", len(creates))
	}
	if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != 0 {
		t.Errorf("got %d notifications with AlsoNotify off, want none", len(notifs))
	}
}

// The first instance decides for the whole alert name: a later instance with
// the other label joins what is already there.
func TestFirstInstanceDecidesActivity(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, Config{ActivityMode: ActivityModeLabeled, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.handleFiring(context.Background(), firingAlert(), nil)
	second := labeledFiring("true")
	second.Fingerprint = "fp2"
	b.handleFiring(context.Background(), second, nil)

	if n := m.activityCalls(); n != 0 {
		t.Errorf("got %d /activities calls, want none", n)
	}
	if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != 1 {
		t.Errorf("got %d notifications, want only the first instance's", len(notifs))
	}

	b.handleResolved(context.Background(), resolvedAlert())
	if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != 1 {
		t.Errorf("got %d notifications with one instance still firing, want no resolved push yet", len(notifs))
	}
}

// Acknowledge follows its own setting for pushes without a Live Activity, not
// AlsoNotify, and the resolve stops the repeats before the resolved push.
func TestNotifyOnlyAckRepeatsAndResolveCancelsFirst(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	cfg := ackConfig()
	cfg.AlsoNotify = false
	cfg.ActivityMode = ActivityModeLabeled
	b := newTestBridge(t, m.url, nil, cfg)
	slug := makeSlug("HighCPU")

	b.handleFiring(context.Background(), firingAlert(), nil)

	notifs, _ := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notifs))
	}
	if _, ok := notifs[0]["acknowledge"].(map[string]any); !ok {
		t.Fatalf("firing push has no acknowledge: %v", notifs[0])
	}
	if tags, _ := notifs[0]["tags"].([]any); len(tags) != 1 || tags[0] != slug {
		t.Errorf("tags = %v, want [%s]", notifs[0]["tags"], slug)
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
}

// With AlsoNotify off, an alert that has a Live Activity sends no push, so its
// resolve has no repeats to stop and makes no cancel call.
func TestActivityAlertWithoutAlsoNotifySkipsAckCancel(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	cfg := ackConfig()
	cfg.AlsoNotify = false
	b := newTestBridge(t, m.url, nil, cfg)

	b.handleFiring(context.Background(), firingAlert(), nil)
	b.handleResolved(context.Background(), resolvedAlert())

	if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != 0 {
		t.Errorf("got %d notifications, want none", len(notifs))
	}
	if cancels, _ := m.requests(t, http.MethodPost, "/notifications/receipts/cancel"); len(cancels) != 0 {
		t.Errorf("got %d cancel requests, want none", len(cancels))
	}
}

// The alertmanager backstop resolves an alert without a Live Activity with a
// push, not an activity update.
func TestBackstopResolvesNotifyOnlyAlertWithPush(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, resolvingResolver{resolve: func() {}}, Config{ActivityMode: ActivityModeLabeled, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.handleFiring(context.Background(), firingAlert(), nil)
	b.checkAlertStates(context.Background())

	notifs, _ := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 2 {
		t.Fatalf("got %d notifications, want firing + resolved", len(notifs))
	}
	if got, _ := notifs[1]["body"].(string); got != "Resolved" {
		t.Errorf("resolved body = %q, want Resolved", got)
	}
	if n := m.activityCalls(); n != 0 {
		t.Errorf("got %d /activities calls, want none", n)
	}
	if n := b.ActiveCount(); n != 0 {
		t.Errorf("ActiveCount = %d, want 0", n)
	}
}

// Ending an activity from the management UI never drops an alert that has no
// Live Activity, even when the slugs match.
func TestForgetSkipsNotifyOnlyAlert(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, Config{ActivityMode: ActivityModeLabeled, SeverityLabel: "severity", DefaultSeverity: "warning"})

	b.handleFiring(context.Background(), firingAlert(), nil)

	if b.Forget(context.Background(), makeSlug("HighCPU")) {
		t.Error("Forget matched an alert without a Live Activity")
	}
	if n := b.ActiveCount(); n != 1 {
		t.Errorf("ActiveCount = %d, want the alert still tracked", n)
	}
}
