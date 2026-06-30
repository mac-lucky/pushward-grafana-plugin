package bridge

import (
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

// TestActiveAlertsAndForget covers the management-UI accessors: ActiveAlerts
// snapshots the tracked alerts (slug/alertname/ruleUID), and Forget drops an
// entry by slug + stops its poller so a user-initiated End isn't resurrected.
func TestActiveAlertsAndForget(t *testing.T) {
	poller := NewPoller(nopQuerier{}, pushward.NewClient("http://127.0.0.1:0", "hlk_x"), time.Hour)
	b := &Bridge{active: make(map[string]*alertState), poller: poller}
	t.Cleanup(func() {
		poller.StopAll()
		poller.Wait()
	})

	b.active["HighCPU"] = &alertState{slug: "grafana-abc", alertname: "HighCPU", ruleUID: "rule-1"}
	b.active["LowDisk"] = &alertState{slug: "grafana-def", alertname: "LowDisk"}

	got := b.ActiveAlerts()
	if len(got) != 2 {
		t.Fatalf("ActiveAlerts len = %d, want 2", len(got))
	}
	bySlug := map[string]ActiveAlert{}
	for _, a := range got {
		bySlug[a.Slug] = a
	}
	if a := bySlug["grafana-abc"]; a.AlertName != "HighCPU" || a.RuleUID != "rule-1" {
		t.Errorf("grafana-abc = %+v, want alertname=HighCPU ruleUid=rule-1", a)
	}

	if !b.Forget("grafana-abc") {
		t.Fatal("Forget returned false for a tracked slug")
	}
	if b.Forget("grafana-abc") {
		t.Error("Forget returned true for an already-removed slug")
	}
	if n := b.ActiveCount(); n != 1 {
		t.Fatalf("ActiveCount = %d after Forget, want 1", n)
	}
	if b.Forget("does-not-exist") {
		t.Error("Forget returned true for an unknown slug")
	}
}
