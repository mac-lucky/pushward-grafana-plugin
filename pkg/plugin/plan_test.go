package plugin

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestCanPollFollowsLiveActivityLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"paid plan omits the limit", `{"id":"x","subscribed":true}`, true},
		{"free plan reports one", `{"id":"x","live_activity_updates_limit":250}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			app := newAppWithStub(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/auth/me" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				calls.Add(1)
				_, _ = w.Write([]byte(tc.body))
			})
			for range 3 {
				if got := app.canPoll(context.Background()); got != tc.want {
					t.Fatalf("canPoll = %v, want %v", got, tc.want)
				}
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("%d /auth/me calls, want 1 (cached)", n)
			}
		})
	}
}

// Until the plan is known the answer is no, so a free account never polls
// because PushWard was briefly unreachable.
func TestCanPollFailsClosed(t *testing.T) {
	app := newAppWithStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	if app.canPoll(context.Background()) {
		t.Error("canPoll = true without ever reading the plan")
	}
}
