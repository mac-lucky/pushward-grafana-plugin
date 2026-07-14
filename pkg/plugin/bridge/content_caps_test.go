package bridge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

// TestBuildContentStateTruncated covers the reported 422: the alert summary (or
// alertname fallback) becomes content.State, which the server rejects past
// maxStateRunes. buildContent must truncate it so the timeline update stays valid.
func TestBuildContentStateTruncated(t *testing.T) {
	b := &Bridge{cfg: Config{}}

	long := strings.Repeat("x", maxStateRunes+44)
	got := b.buildContent(alert{Annotations: map[string]string{annSummary: long}}, "warning", nil)
	if n := utf8.RuneCountInString(got.State); n != maxStateRunes {
		t.Errorf("summary State: got %d runes, want %d", n, maxStateRunes)
	}

	// Alertname fallback (no summary) is capped the same way.
	got = b.buildContent(alert{Labels: map[string]string{"alertname": long}}, "warning", nil)
	if n := utf8.RuneCountInString(got.State); n != maxStateRunes {
		t.Errorf("alertname State: got %d runes, want %d", n, maxStateRunes)
	}

	// A short summary passes through untouched.
	got = b.buildContent(alert{Annotations: map[string]string{annSummary: "DaemonSet not ready"}}, "warning", nil)
	if got.State != "DaemonSet not ready" {
		t.Errorf("short State altered: got %q", got.State)
	}
}

// TestBuildContentUnitTruncated covers the pushward_unit annotation against the
// server's maxUnitRunes cap. TruncateHard is used so a unit label gains no "..."
// suffix.
func TestBuildContentUnitTruncated(t *testing.T) {
	b := &Bridge{cfg: Config{}}

	unit := strings.Repeat("u", maxUnitRunes+8)
	got := b.buildContent(alert{Annotations: map[string]string{annUnit: unit}}, "warning", nil)
	if n := utf8.RuneCountInString(got.Unit); n != maxUnitRunes {
		t.Errorf("Unit: got %d runes, want %d", n, maxUnitRunes)
	}
	if strings.HasSuffix(got.Unit, "...") {
		t.Errorf("Unit should be hard-truncated (no ellipsis), got %q", got.Unit)
	}

	got = b.buildContent(alert{Annotations: map[string]string{annUnit: "req/s"}}, "warning", nil)
	if got.Unit != "req/s" {
		t.Errorf("short Unit altered: got %q", got.Unit)
	}
}

// TestResolveValuesKeyCapped covers the value-map key path: a long alertname
// becomes a timeline value key, which the server rejects past maxSeriesKeyRunes.
// This is the default-config 422 vector (no history fetch -> resolveValues).
func TestResolveValuesKeyCapped(t *testing.T) {
	b := &Bridge{cfg: Config{}}
	long := strings.Repeat("K", maxSeriesKeyRunes+8)

	values := b.resolveValues(alert{
		Labels: map[string]string{"alertname": long},
		Values: map[string]float64{"A": 1},
	}, "", "")

	if len(values) != 1 {
		t.Fatalf("got %d keys, want 1", len(values))
	}
	for k := range values {
		if n := utf8.RuneCountInString(k); n > maxSeriesKeyRunes {
			t.Errorf("value key %q has %d runes, exceeds %d", k, n, maxSeriesKeyRunes)
		}
	}
}

// TestCapSeries covers the server's MaxTimelineSeries cap: over-limit maps are
// trimmed to exactly the limit with a deterministic lexicographic selection, the
// primary series is always retained even when it sorts last, a missing primary
// is a no-op, and an at-limit map is returned unchanged.
func TestCapSeries(t *testing.T) {
	values := make(map[string]float64, 15)
	for i := 0; i < 15; i++ {
		values[key(i)] = float64(i)
	}

	// primary sorts last (s14) yet must survive; the rest are the 9 smallest keys.
	got, trimmed := capSeries(values, key(14))
	if !trimmed {
		t.Fatal("expected trimmed=true for 15 series")
	}
	wantWithPrimary := append(keysRange(0, 9), key(14)) // s00..s08 + s14
	if !sameKeys(got, wantWithPrimary) {
		t.Errorf("with primary: got keys %v, want %v", sortedKeys(got), wantWithPrimary)
	}

	// primary absent from values -> plain lexicographic-smallest selection.
	gotAbsent, _ := capSeries(values, "not-a-key")
	if !sameKeys(gotAbsent, keysRange(0, maxTimelineSeries)) {
		t.Errorf("absent primary: got %v, want the %d smallest keys", sortedKeys(gotAbsent), maxTimelineSeries)
	}

	// Determinism independent of insertion order: a differently-ordered map with
	// the same keys yields the same selection (proves the sort, not luck).
	reordered := make(map[string]float64, 15)
	for i := 14; i >= 0; i-- {
		reordered[key(i)] = float64(i)
	}
	gotReordered, _ := capSeries(reordered, "")
	if !sameKeys(gotReordered, keysRange(0, maxTimelineSeries)) {
		t.Errorf("reordered map selection differs: %v", sortedKeys(gotReordered))
	}

	// At/under the limit: returned unchanged (contents equal, trimmed=false).
	small := map[string]float64{"a": 1, "b": 2}
	gotSmall, trimmedSmall := capSeries(small, "")
	if trimmedSmall || !reflect.DeepEqual(gotSmall, small) {
		t.Errorf("under limit: trimmed=%v got=%v, want unchanged", trimmedSmall, gotSmall)
	}
}

// TestCapHistory covers aligning history to the kept value keys and the no-op
// fast-path when every history key already fits.
func TestCapHistory(t *testing.T) {
	history := map[string][]pushward.HistoryPoint{
		"a": {{Timestamp: 1, Value: 1}},
		"b": {{Timestamp: 1, Value: 2}},
		"c": {{Timestamp: 1, Value: 3}},
	}

	got := capHistory(history, map[string]float64{"a": 1, "b": 2})
	if _, ok := got["c"]; ok || len(got) != 2 {
		t.Fatalf("got keys %v, want [a b]", historyKeys(got))
	}

	// keep is a superset -> every history key survives, contents unchanged.
	gotSame := capHistory(history, map[string]float64{"a": 1, "b": 2, "c": 3, "d": 4})
	if !reflect.DeepEqual(gotSame, history) {
		t.Error("superset keep should leave history unchanged")
	}

	if got := capHistory(map[string][]pushward.HistoryPoint{}, map[string]float64{"a": 1}); len(got) != 0 {
		t.Error("empty history should stay empty")
	}
}

// TestCapSeriesThenHistoryAligned is the invariant that actually prevents a
// second 422 / orphaned-series pruning: after capSeries trims the value map,
// capHistory must leave history keyed by exactly the kept value keys.
func TestCapSeriesThenHistoryAligned(t *testing.T) {
	values := make(map[string]float64, 15)
	history := make(map[string][]pushward.HistoryPoint, 15)
	for i := 0; i < 15; i++ {
		values[key(i)] = float64(i)
		history[key(i)] = []pushward.HistoryPoint{{Timestamp: 1, Value: float64(i)}}
	}

	capped, _ := capSeries(values, key(14))
	h := capHistory(history, capped)

	if len(h) != maxTimelineSeries {
		t.Fatalf("history has %d series, want %d", len(h), maxTimelineSeries)
	}
	if !reflect.DeepEqual(sortedKeys(capped), historyKeys(h)) {
		t.Errorf("history keys %v not aligned with value keys %v", historyKeys(h), sortedKeys(capped))
	}
	if _, ok := h[key(14)]; !ok {
		t.Error("primary series history was dropped")
	}
}

// TestPollCapsSeriesAndKeepsPrimary exercises the per-tick cap in the poller
// (the reason primary is threaded through Start/run/poll): a poll that yields
// more than the limit must send/record exactly maxTimelineSeries keys including
// the primary, so the ongoing update can't re-introduce the 422.
func TestPollCapsSeriesAndKeepsPrimary(t *testing.T) {
	points := make([]LabeledPoint, 0, 12)
	points = append(points, LabeledPoint{
		Labels: map[string]string{"instance": "zzz-primary"},
		Point:  pushward.HistoryPoint{Timestamp: 1, Value: 1},
	})
	for i := 0; i < 11; i++ {
		points = append(points, LabeledPoint{
			Labels: map[string]string{"instance": key(i)},
			Point:  pushward.HistoryPoint{Timestamp: 1, Value: float64(i)},
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p := NewPoller(fixedPointsQuerier{points}, pushward.NewClient(srv.URL, "hlk_x"), time.Hour)
	var gotValues map[string]float64
	p.SetUpdateCallback(func(_ string, values map[string]float64) { gotValues = values })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// seeded=true -> the value-only patch path, which is the steady-state tick.
	p.poll(context.Background(), logger, "slug", "up", "", "zzz-primary", nil, true)

	if len(gotValues) != maxTimelineSeries {
		t.Fatalf("poll recorded %d series, want %d", len(gotValues), maxTimelineSeries)
	}
	if _, ok := gotValues["zzz-primary"]; !ok {
		t.Error("poll dropped the primary series past the cap")
	}
}

type fixedPointsQuerier struct{ points []LabeledPoint }

func (fixedPointsQuerier) QueryRangeAll(context.Context, string, time.Time, time.Time, time.Duration) ([]LabeledSeries, error) {
	return nil, nil
}
func (q fixedPointsQuerier) QueryInstantAll(context.Context, string, time.Time) ([]LabeledPoint, error) {
	return q.points, nil
}

func key(i int) string { return fmt.Sprintf("s%02d", i) }

func keysRange(start, end int) []string {
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, key(i))
	}
	return out
}

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func historyKeys(m map[string][]pushward.HistoryPoint) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sameKeys(m map[string]float64, want []string) bool {
	got := sortedKeys(m)
	sort.Strings(want)
	return reflect.DeepEqual(got, want)
}
