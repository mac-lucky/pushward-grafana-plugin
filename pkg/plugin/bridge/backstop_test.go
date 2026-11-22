package bridge

import (
	"context"
	"errors"
	"testing"

	"github.com/mac-lucky/pushward-integrations/shared/testutil"
)

// listResolver answers the backstop with a fixed alert list and counts how
// often it was asked.
type listResolver struct {
	token bool
	names map[string]bool
	err   error
	asked *int
}

func (listResolver) ExtractRuleUID(string) string { return "" }

func (listResolver) GetRuleQuery(context.Context, string) (string, string, string, error) {
	return "", "", "", errors.New("no query")
}

func (r listResolver) HasToken() bool { return r.token }

func (r listResolver) FiringAlertNames(context.Context) (map[string]bool, error) {
	*r.asked++
	return r.names, r.err
}

// The backstop asks Grafana once per sweep, ends only the tracked alerts the
// list leaves out, and ends none without a token or a list it could read.
func TestBackstopSweep(t *testing.T) {
	cases := []struct {
		name      string
		r         listResolver
		wantAsked int
		wantEnded bool
	}{
		{"still firing", listResolver{token: true, names: map[string]bool{"HighCPU": true}}, 1, false},
		{"no longer firing", listResolver{token: true, names: map[string]bool{"Other": true}}, 1, true},
		{"list unreadable", listResolver{token: true, err: errors.New("decoding alerts: unexpected EOF")}, 1, false},
		{"no IAM token", listResolver{}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t, testutil.MockOptions{})
			asked := 0
			tc.r.asked = &asked
			b := newTestBridge(t, m.url, tc.r, Config{SeverityLabel: "severity", DefaultSeverity: "warning"})
			trackHighCPU(t, b, "fp1")
			anon := trackedHighCPU("fp2")
			anon.alertname, anon.slug = "", makeSlug("fp2")
			b.active["fp2"] = anon // no name to look up: left to the stale sweep

			b.checkAlertStates(context.Background())

			if asked != tc.wantAsked {
				t.Errorf("asked Grafana %d times, want %d", asked, tc.wantAsked)
			}
			if _, ended := b.active["HighCPU"]; ended == tc.wantEnded {
				t.Errorf("HighCPU tracked = %v, want ended = %v", ended, tc.wantEnded)
			}
			if got := len(endedPatches(t, m)); (got > 0) != tc.wantEnded {
				t.Errorf("%d ENDED updates, want ended = %v", got, tc.wantEnded)
			}
			if _, ok := b.active["fp2"]; !ok {
				t.Error("the backstop ended an alert without a name")
			}
		})
	}
}
