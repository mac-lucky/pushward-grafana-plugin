package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

// nopQuerier is a MetricsQuerier that returns no data; the guard test never
// lets a poll fire (interval is long and we stop immediately), so it is only a
// placeholder to construct a Poller.
type nopQuerier struct{}

func (nopQuerier) QueryRangeAll(context.Context, string, time.Time, time.Time, time.Duration) ([]LabeledSeries, error) {
	return nil, nil
}
func (nopQuerier) QueryInstantAll(context.Context, string, time.Time) ([]LabeledPoint, error) {
	return nil, nil
}

// TestStartPollerIfTracked is the regression guard for the orphan-poller race:
// the per-alert poller must start only while the alert is still tracked, so a
// resolved webhook that already removed the entry can't be followed by a Start
// that leaves a poller running against an ended activity.
func TestStartPollerIfTracked(t *testing.T) {
	poller := NewPoller(nopQuerier{}, pushward.NewClient("http://127.0.0.1:0", "hlk_x"), time.Hour)
	b := &Bridge{active: make(map[string]*alertState), poller: poller}
	t.Cleanup(func() {
		poller.StopAll()
		poller.Wait()
	})

	// Entry absent (resolved/swept already): must not start a poller.
	if b.startPollerIfTracked("key", "slug", "up", "", nil) {
		t.Fatal("started a poller for an alert that is no longer tracked")
	}
	if n := poller.ActiveCount(); n != 0 {
		t.Fatalf("ActiveCount = %d, want 0 after a skipped start", n)
	}

	// Entry present: must start exactly one poller.
	b.active["key"] = &alertState{slug: "slug"}
	if !b.startPollerIfTracked("key", "slug", "up", "", nil) {
		t.Fatal("did not start a poller for a tracked alert")
	}
	if n := poller.ActiveCount(); n != 1 {
		t.Fatalf("ActiveCount = %d, want 1 after a tracked start", n)
	}
}
