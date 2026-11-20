package bridge

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	"github.com/mac-lucky/pushward-integrations/shared/testutil"
)

// firingOnMock puts HighCPU's timeline on the mock the way the instance before
// a settings save left it: created and showing a firing value.
func firingOnMock(t *testing.T, b *Bridge) {
	t.Helper()
	ctx := context.Background()
	slug := makeSlug("HighCPU")
	if err := b.pwClient.CreateActivity(ctx, slug, "HighCPU", 5, 0, 0); err != nil {
		t.Fatal(err)
	}
	content := b.buildContent(firingAlert(), "critical", map[string]float64{"HighCPU": 97})
	if err := b.pwClient.UpdateActivity(ctx, slug, pushward.UpdateRequest{State: pushward.StateOngoing, Content: content}); err != nil {
		t.Fatal(err)
	}
}

// endedPatches returns the ENDED updates sent to HighCPU's activity.
func endedPatches(t *testing.T, m mockServer) []map[string]any {
	t.Helper()
	patches, _ := m.requests(t, http.MethodPatch, "/activities/"+makeSlug("HighCPU"))
	var out []map[string]any
	for _, p := range patches {
		if p["state"] == pushward.StateEnded {
			out = append(out, p)
		}
	}
	return out
}

// A resolve the instance does not track, as after a settings save, still ends
// the Live Activity the previous instance left firing, and sends the resolved
// push when AlsoNotify asks for one and retract does not take it back instead.
func TestUntrackedResolveEndsActivity(t *testing.T) {
	slug := makeSlug("HighCPU")
	cases := []struct {
		name       string
		cfg        Config
		wantPush   bool
		wantQueued int
	}{
		{"also notify off", Config{SeverityLabel: "severity", DefaultSeverity: "warning"}, false, 0},
		{"also notify on", ackConfig(), true, 0},
		{"also notify with retract on resolve", retractConfig(), false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			b := newTestBridge(t, m.url, nil, tc.cfg)
			firingOnMock(t, b)

			b.handleResolved(context.Background(), resolvedAlert(), map[string]bool{})

			ended := endedPatches(t, m)
			if len(ended) != 1 {
				t.Fatalf("got %d ENDED updates, want 1", len(ended))
			}
			content, _ := ended[0]["content"].(map[string]any)
			if content["icon"] != resolvedIcon || content["accent_color"] != pushward.ColorGreen {
				t.Errorf("ENDED content icon/color = %v/%v, want %s/%s", content["icon"], content["accent_color"], resolvedIcon, pushward.ColorGreen)
			}
			if _, ok := content["value"]; ok {
				t.Errorf("ENDED content carries value %v, want none so the stored series survive", content["value"])
			}
			if content["state"] != "CPU back to normal" {
				t.Errorf("ENDED content state = %v, want the resolve's summary", content["state"])
			}

			notifs, _ := m.requests(t, http.MethodPost, "/notifications")
			if !tc.wantPush {
				if len(notifs) != 0 {
					t.Errorf("got %d notifications, want none", len(notifs))
				}
			} else {
				if len(notifs) != 1 {
					t.Fatalf("got %d notifications, want the resolved push", len(notifs))
				}
				if body, _ := notifs[0]["body"].(string); !strings.HasPrefix(body, "Resolved") {
					t.Errorf("push body = %q, want the resolved push", body)
				}
				if notifs[0]["activity_slug"] != slug {
					t.Errorf("push activity_slug = %v, want %s (the end went through)", notifs[0]["activity_slug"], slug)
				}
			}
			if got := len(b.retracts); got != tc.wantQueued {
				t.Errorf("%d retracts queued, want %d", got, tc.wantQueued)
			}
		})
	}
}

// No activity on the server (never created, or already gone) is not an error:
// the resolved push still goes out, without a link to the missing activity.
func TestUntrackedResolveActivityGone(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, ackConfig())
	dl := &recLog{}
	b.deliveryLog = dl

	b.handleResolved(context.Background(), resolvedAlert(), nil)

	if patches, _ := m.requests(t, http.MethodPatch, "/activities/"+makeSlug("HighCPU")); len(patches) != 1 {
		t.Fatalf("got %d PATCH requests, want the one ENDED attempt", len(patches))
	}
	if e := dl.find("resolved"); e != nil {
		t.Errorf("delivery log has %+v for an activity that does not exist, want nothing", e)
	}
	notifs, _ := m.requests(t, http.MethodPost, "/notifications")
	if len(notifs) != 1 {
		t.Fatalf("got %d notifications, want the resolved push", len(notifs))
	}
	if s, _ := notifs[0]["activity_slug"].(string); s != "" {
		t.Errorf("push activity_slug = %q, want none for a missing activity", s)
	}
}

// What an untracked resolve leaves alone: an alert without a Live Activity,
// an alert name that fires again in the same payload, and an activity this
// instance ended already (the backstop, End, an earlier resolve).
func TestUntrackedResolveSkips(t *testing.T) {
	cases := []struct {
		name      string
		before    func(t *testing.T, b *Bridge)
		a         alert
		noRetract map[string]bool
		wantEnded int
		wantPush  int
	}{
		{"alert without a Live Activity", nil, func() alert {
			a := resolvedAlert()
			a.Labels[LabelActivity] = "false"
			return a
		}(), nil, 0, 0},
		{"fires again in the same payload", nil, resolvedAlert(),
			map[string]bool{alertCollapseID("HighCPU"): true}, 0, 0},
		{"ended by the alertmanager backstop", func(t *testing.T, b *Bridge) {
			b.grafanaClient = resolvingResolver{}
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.checkAlertStates(context.Background())
		}, resolvedAlert(), nil, 1, 1},
		{"ended from the Activities page", func(t *testing.T, b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.Forget(context.Background(), makeSlug("HighCPU"))
		}, resolvedAlert(), nil, 0, 0},
		{"ended by an earlier untracked resolve", func(t *testing.T, b *Bridge) {
			b.handleResolved(context.Background(), resolvedAlert(), nil)
		}, resolvedAlert(), nil, 1, 1},
		{"ended by the resolve of a tracked alert", func(t *testing.T, b *Bridge) {
			b.active["HighCPU"] = trackedHighCPU("fp1")
			b.handleResolved(context.Background(), resolvedAlert(), nil)
		}, resolvedAlert(), nil, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			cfg := ackConfig()
			cfg.Ack = false // no receipts to cancel; only ends and pushes count
			b := newTestBridge(t, m.url, nil, cfg)
			firingOnMock(t, b)
			if tc.before != nil {
				tc.before(t, b)
			}

			b.handleResolved(context.Background(), tc.a, tc.noRetract)

			if got := len(endedPatches(t, m)); got != tc.wantEnded {
				t.Errorf("got %d ENDED updates, want %d", got, tc.wantEnded)
			}
			if notifs, _ := m.requests(t, http.MethodPost, "/notifications"); len(notifs) != tc.wantPush {
				t.Errorf("got %d notifications, want %d", len(notifs), tc.wantPush)
			}
		})
	}
}

// The resolve of an alert fired again after its untracked end ends it again:
// the firing alert took the slug back.
func TestUntrackedResolveAfterRefire(t *testing.T) {
	m := newMockServer(t, testutil.MockOptions{})
	b := newTestBridge(t, m.url, nil, Config{SeverityLabel: "severity", DefaultSeverity: "warning"})
	firingOnMock(t, b)

	b.handleResolved(context.Background(), resolvedAlert(), nil)
	again := firingAlert()
	again.Values = map[string]float64{"A": 97}
	b.handleFiring(context.Background(), again, nil)
	b.mu.Lock()
	delete(b.active, "HighCPU") // a settings save again
	b.mu.Unlock()
	b.handleResolved(context.Background(), resolvedAlert(), nil)

	if got := len(endedPatches(t, m)); got != 2 {
		t.Errorf("got %d ENDED updates, want one per resolve", got)
	}
}
