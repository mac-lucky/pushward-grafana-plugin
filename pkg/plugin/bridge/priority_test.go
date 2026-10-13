package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func severityCfg(configured int, fromSeverity bool) Config {
	return Config{
		Priority: configured, PriorityFromSeverity: fromSeverity,
		SeverityLabel: "severity", DefaultSeverity: "warning",
	}
}

// alertWith is firingAlert with the severity label set to sev ("" leaves it
// off), its own fingerprint, and a value so the update path runs.
func alertWith(fp, sev string) alert {
	a := firingAlert()
	a.Fingerprint = fp
	a.Labels = map[string]string{"alertname": "HighCPU"}
	if sev != "" {
		a.Labels["severity"] = sev
	}
	a.Values = map[string]float64{"A": 1}
	return a
}

// patches returns the priority sent with each PATCH, -1 where it was left out.
func (s *stubServer) patches() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int
	for _, r := range s.reqs {
		if r.method != http.MethodPatch {
			continue
		}
		p, ok := r.body["priority"].(float64)
		if !ok {
			p = -1
		}
		out = append(out, int(p))
	}
	return out
}

func storedPriority(b *Bridge, key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active[key].priority
}

func createdPriority(t *testing.T, s *stubServer) int {
	t.Helper()
	create := s.find(http.MethodPost, "/activities")
	if create == nil {
		t.Fatal("expected a POST /activities")
	}
	p, ok := create.body["priority"].(float64)
	if !ok {
		t.Fatalf("POST /activities has no priority: %v", create.body)
	}
	return int(p)
}

func TestPriorityFollowsSeverity(t *testing.T) {
	cases := []struct {
		name         string
		severity     string
		configured   int
		fromSeverity bool
		want         int
	}{
		{"critical", "critical", 5, true, priorityCritical},
		{"critical in another case", "Critical", 5, true, priorityCritical},
		{"warning", "warning", 5, true, 5},
		{"info", "INFO", 5, true, priorityLow},
		{"none", "none", 5, true, priorityLow},
		{"unknown value", "page", 5, true, 5},
		{"no label", "", 5, true, 5},
		{"info never above the configured priority", "info", 0, true, 0},
		{"critical above a high configured priority", "critical", 9, true, priorityCritical},
		{"off keeps the configured priority", "critical", 5, false, 5},
		{"off keeps it for info too", "info", 5, false, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubServer(t)
			b := newTestBridge(t, s.URL, nil, severityCfg(tc.configured, tc.fromSeverity))

			b.handleFiring(context.Background(), alertWith("fp1", tc.severity), nil)

			if got := createdPriority(t, s); got != tc.want {
				t.Errorf("priority = %d, want %d", got, tc.want)
			}
		})
	}
}

// A critical instance joining a warning one raises the activity, and a later
// warning instance does not lower it again.
func TestPriorityRaisedByMoreSevereInstance(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, severityCfg(5, true))
	ctx := context.Background()

	b.handleFiring(ctx, alertWith("fp1", "warning"), nil)
	b.handleFiring(ctx, alertWith("fp2", "critical"), nil)
	b.handleFiring(ctx, alertWith("fp3", "warning"), nil)

	if got := createdPriority(t, s); got != 5 {
		t.Errorf("created with priority %d, want 5", got)
	}
	want := []int{-1, priorityCritical, -1}
	got := s.patches()
	if len(got) != len(want) {
		t.Fatalf("PATCH priorities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PATCH priorities = %v, want %v", got, want)
		}
	}
	if p := storedPriority(b, "HighCPU"); p != priorityCritical {
		t.Errorf("stored priority = %d, want %d", p, priorityCritical)
	}
}

// Without values there is no timeline update to carry the raise, so it goes
// out as a priority-only patch.
func TestPriorityRaisedWithoutValues(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, severityCfg(5, true))
	ctx := context.Background()

	first, second := alertWith("fp1", "warning"), alertWith("fp2", "critical")
	first.Values, second.Values = nil, nil
	b.handleFiring(ctx, first, nil)
	b.handleFiring(ctx, second, nil)

	got := s.patches()
	if len(got) != 1 || got[0] != priorityCritical {
		t.Fatalf("PATCH priorities = %v, want [%d]", got, priorityCritical)
	}
	if p := storedPriority(b, "HighCPU"); p != priorityCritical {
		t.Errorf("stored priority = %d, want %d", p, priorityCritical)
	}
}

// With the setting off a critical instance joining never touches the priority.
func TestPriorityOffNeverRaises(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, severityCfg(5, false))
	ctx := context.Background()

	b.handleFiring(ctx, alertWith("fp1", "warning"), nil)
	b.handleFiring(ctx, alertWith("fp2", "critical"), nil)

	for _, p := range s.patches() {
		if p != -1 {
			t.Fatalf("PATCH priorities = %v, want none set", s.patches())
		}
	}
}

// After a settings save the bridge starts empty, so the next webhook creates
// the activity again: it has to start at the payload's most severe instance,
// not at whichever Grafana listed first.
func TestPriorityRecreateTakesPayloadMax(t *testing.T) {
	s := newStubServer(t)
	b := newTestBridge(t, s.URL, nil, severityCfg(5, true))

	raw, err := json.Marshal(webhookPayload{Status: alertStatusFiring, Alerts: []alert{
		alertWith("fp1", "warning"),
		alertWith("fp2", "critical"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ProcessWebhook(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	b.wg.Wait()

	if got := createdPriority(t, s); got != priorityCritical {
		t.Errorf("created with priority %d, want %d", got, priorityCritical)
	}
	for _, p := range s.patches() {
		if p != -1 {
			t.Errorf("PATCH priorities = %v, want none set", s.patches())
			break
		}
	}
}

func TestResolveSeverityIgnoresCase(t *testing.T) {
	b := newTestBridge(t, "http://unused", nil, severityCfg(5, false))
	cases := map[string]string{
		"Critical": "critical",
		"WARNING":  "warning",
		"Info":     "info",
		"none":     "info",
		"NONE":     "info",
		"page":     "warning",
		"":         "warning",
	}
	for label, want := range cases {
		if got := b.resolveSeverity(alertWith("fp1", label)); got != want {
			t.Errorf("severity %q resolves to %q, want %q", label, got, want)
		}
	}
}
