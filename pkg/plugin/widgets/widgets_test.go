package widgets

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	sharedwidgets "github.com/mac-lucky/pushward-integrations/shared/widgets"

	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/bridge"
)

// stubQuerier returns canned instant-query points keyed by expression. err fails
// every query; errByExpr fails only the listed expressions (for partial-outage
// tests).
type stubQuerier struct {
	byExpr    map[string][]bridge.LabeledPoint
	errByExpr map[string]error
	err       error
}

func (s *stubQuerier) QueryInstantAll(_ context.Context, expr string, _ time.Time) ([]bridge.LabeledPoint, error) {
	if s.err != nil {
		return nil, s.err
	}
	if e, ok := s.errByExpr[expr]; ok {
		return nil, e
	}
	return s.byExpr[expr], nil
}

func point(v float64, labels map[string]string) bridge.LabeledPoint {
	return bridge.LabeledPoint{Labels: labels, Point: pushward.HistoryPoint{Timestamp: 1, Value: v}}
}

func TestParseWidgetsEmpty(t *testing.T) {
	for _, in := range []string{"", "null", "[]", "  "} {
		w, err := ParseWidgets(json.RawMessage(in))
		if err != nil || w != nil {
			t.Errorf("ParseWidgets(%q) = (%v, %v), want (nil, nil)", in, w, err)
		}
	}
}

func TestParseWidgetsValidProductionShape(t *testing.T) {
	// A representative production config: a stat_list with trigger masks and a
	// gauge with bounds and a duration-string interval.
	raw := `[
	  {"slug":"pushward-stats","name":"PushWard","template":"stat_list","interval":"60s","update_mode":"on_change",
	   "content":{"icon":"chart.bar.fill","severity":"info"},
	   "stat_rows":[
	     {"label":"Registered Users","query":"users_total","value_template":"{{printf \"%.0f\" .Value}}"},
	     {"label":"Trials","query":"trials","value_template":"{{printf \"%.0f\" .Value}}","trigger":false}
	   ]},
	  {"slug":"pushward-http-5xx-rate","name":"HTTP 5xx","template":"gauge","query":"rate5xx","interval":"1h",
	   "update_mode":"on_change","min_change":0.05,
	   "content":{"icon":"server.rack","unit":"%","min_value":0.0,"max_value":2.0,"severity":"info"}}
	]`
	widgets, err := ParseWidgets(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseWidgets: %v", err)
	}
	if len(widgets) != 2 {
		t.Fatalf("got %d widgets, want 2", len(widgets))
	}
	if got := widgets[0].IntervalDuration(); got != 60*time.Second {
		t.Errorf("stat_list interval = %v, want 60s", got)
	}
	if got := widgets[1].IntervalDuration(); got != time.Hour {
		t.Errorf("gauge interval = %v, want 1h", got)
	}
}

func TestValidateRejectsBadConfigs(t *testing.T) {
	cases := map[string]string{
		"missing slug":          `[{"template":"value","query":"up"}]`,
		"bad slug":              `[{"slug":"Bad Slug","template":"value","query":"up"}]`,
		"duplicate slug":        `[{"slug":"a","query":"up"},{"slug":"a","query":"up"}]`,
		"unknown template":      `[{"slug":"a","template":"bogus","query":"up"}]`,
		"gauge needs bounds":    `[{"slug":"a","template":"gauge","query":"up"}]`,
		"stat_list needs rows":  `[{"slug":"a","template":"stat_list"}]`,
		"interval too short":    `[{"slug":"a","query":"up","interval":"1s"}]`,
		"query_all needs slugt": `[{"slug":"a","query_all":"up"}]`,
		"all rows non-trigger":  `[{"slug":"a","template":"stat_list","stat_rows":[{"label":"x","query":"q","value_template":"{{.Value}}","trigger":false}]}]`,
		"unknown field":         `[{"slug":"a","query":"up","nope":1}]`,

		"trend needs query":       `[{"slug":"a","template":"trend"}]`,
		"trend rejects fan-out":   `[{"slug":"a","template":"trend","query_all":"up","slug_template":"i-{{.instance}}"}]`,
		"countdown needs end":     `[{"slug":"a","template":"countdown"}]`,
		"countdown rejects query": `[{"slug":"a","template":"countdown","query":"up","content":{"end_date":"2026-12-24T18:00:00Z"}}]`,
		"end_date not rfc3339":    `[{"slug":"a","template":"countdown","content":{"end_date":"24/12/2026"}}]`,
		"end_date in 1970":        `[{"slug":"a","template":"countdown","content":{"end_date":"1970-01-02T00:00:00Z"}}]`,
		"start after end":         `[{"slug":"a","template":"countdown","content":{"start_date":"2026-12-25T18:00:00Z","end_date":"2026-12-24T18:00:00Z"}}]`,
		"expired_text too long":   `[{"slug":"a","template":"countdown","content":{"end_date":"2026-12-24T18:00:00Z","expired_text":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]`,
		"bad timer style":         `[{"slug":"a","query":"up","content":{"subtitle_timer":{"date":"2026-12-24T18:00:00Z","style":"stopwatch"}}}]`,
		"timer without date":      `[{"slug":"a","query":"up","content":{"subtitle_timer":{"style":"relative"}}}]`,
		"min not below max":       `[{"slug":"a","template":"trend","query":"up","content":{"min_value":5,"max_value":5}}]`,
		"stale_after too small":   `[{"slug":"a","query":"up","stale_after":30}]`,
		"stale_after too large":   `[{"slug":"a","query":"up","stale_after":900000}]`,
		"stale_after under 3x":    `[{"slug":"a","query":"up","interval":"60s","stale_after":120}]`,
		"stale_after on countdown": `[{"slug":"a","template":"countdown","stale_after":300,
		   "content":{"end_date":"2026-12-24T18:00:00Z"}}]`,
	}
	for name, raw := range cases {
		if _, err := ParseWidgets(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

func TestBuildSpecsAttachesSources(t *testing.T) {
	raw := `[
	  {"slug":"scalar","query":"up"},
	  {"slug":"multi","query_all":"by_inst","slug_template":"i-{{.instance}}"},
	  {"slug":"sl","template":"stat_list","stat_rows":[{"label":"x","query":"q","value_template":"{{printf \"%.0f\" .Value}}"}]}
	]`
	widgets, err := ParseWidgets(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseWidgets: %v", err)
	}
	specs, err := BuildSpecs(widgets, &stubQuerier{})
	if err != nil {
		t.Fatalf("BuildSpecs: %v", err)
	}
	if specs[0].Source == nil {
		t.Error("scalar widget should have a Source")
	}
	if specs[1].MultiSource == nil {
		t.Error("query_all widget should have a MultiSource")
	}
	if specs[2].StatListSource == nil {
		t.Error("stat_list widget should have a StatListSource")
	}
}

func TestBuildSpecsTrendAndCountdown(t *testing.T) {
	raw := `[
	  {"slug":"rps","template":"trend","query":"rate5m","interval":"60s","stale_after":300,
	   "content":{"unit":"req/s","min_value":0,"max_value":500}},
	  {"slug":"launch","template":"countdown","name":"Launch",
	   "content":{"end_date":"2026-12-24T18:00:00Z","expired_text":"Shipped","icon":"party.popper"}}
	]`
	cfgs, err := ParseWidgets(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseWidgets: %v", err)
	}
	specs, err := BuildSpecs(cfgs, &stubQuerier{})
	if err != nil {
		t.Fatalf("BuildSpecs: %v", err)
	}

	if _, ok := specs[0].Source.(*TrendSource); !ok {
		t.Errorf("trend widget source = %T, want *TrendSource", specs[0].Source)
	}
	if specs[0].StaleAfter == nil || *specs[0].StaleAfter != 300 {
		t.Errorf("stale_after = %v, want 300", specs[0].StaleAfter)
	}
	if got := specs[0].Heartbeat; got != 150*time.Second {
		t.Errorf("heartbeat = %v, want 150s (half the staleness window)", got)
	}

	if specs[1].Source == nil {
		t.Fatal("countdown widget needs a source; the manager rejects a spec without one")
	}
	if _, err := specs[1].Source.Value(context.Background()); err != sharedwidgets.ErrNoData {
		t.Errorf("countdown source err = %v, want ErrNoData (nothing to poll)", err)
	}
	c := specs[1].Content
	if c.EndDate == nil || c.EndDate.Format(time.RFC3339) != "2026-12-24T18:00:00Z" {
		t.Errorf("end_date = %v, want the parsed config date", c.EndDate)
	}
	if c.ExpiredText != "Shipped" {
		t.Errorf("expired_text = %q, want %q", c.ExpiredText, "Shipped")
	}
	if specs[1].Heartbeat != 0 {
		t.Errorf("countdown heartbeat = %v, want 0", specs[1].Heartbeat)
	}
}

// The validator has to keep three poll intervals inside the window, because the
// heartbeat lands on a poll tick and the worst-case gap is half the window plus
// one interval.
func TestStaleAfterAcceptsThreeIntervals(t *testing.T) {
	raw := `[{"slug":"a","query":"up","interval":"60s","stale_after":180}]`
	cfgs, err := ParseWidgets(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("stale_after at exactly 3x the interval should be accepted: %v", err)
	}
	specs, err := BuildSpecs(cfgs, &stubQuerier{})
	if err != nil {
		t.Fatalf("BuildSpecs: %v", err)
	}
	// Worst case is one heartbeat plus one interval; it has to stay under the window.
	if worst := specs[0].Heartbeat + cfgs[0].IntervalDuration(); worst >= 180*time.Second {
		t.Errorf("worst-case refresh gap %v reaches the 180s window; the widget would dim", worst)
	}
}

func TestTrendSourceBuffersUntilTwoPoints(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"rate": {point(1, nil)}}}
	s := NewTrendSource(q, "rate", time.Minute)

	if _, err := s.Value(context.Background()); err != sharedwidgets.ErrNoData {
		t.Errorf("first Value err = %v, want ErrNoData (one sample is not a line)", err)
	}
	if got := s.Points(); got != nil {
		t.Errorf("Points with one sample = %v, want nil", got)
	}

	q.byExpr["rate"] = []bridge.LabeledPoint{point(2, nil)}
	v, err := s.Value(context.Background())
	if err != nil {
		t.Fatalf("second Value: %v", err)
	}
	if v != 2 {
		t.Errorf("Value = %v, want the latest sample 2", v)
	}
	if got := s.Points(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("Points = %v, want [1 2] oldest first", got)
	}
}

// A repeat inside the gap is dropped, so a fast-polled flat metric cannot flush
// real history out of a 48-slot buffer in minutes. It also keeps the payload
// byte-identical between polls, which is what lets a heartbeat land as a
// server-side touch instead of a push.
func TestTrendSourceSkipsRepeatsInsideTheGap(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"flat": {point(7, nil)}}}
	s := NewTrendSource(q, "flat", 5*time.Second) // gap floors at 60s
	for range 20 {
		if _, err := s.Value(context.Background()); err != nil && err != sharedwidgets.ErrNoData {
			t.Fatalf("Value: %v", err)
		}
	}
	if got := s.Points(); len(got) != trendMinPoints {
		t.Errorf("buffered %v, want only the %d bootstrap samples; repeats inside the gap must be dropped", got, trendMinPoints)
	}
}

// Past the gap a repeat records again, so a long flat stretch still advances
// rather than freezing the chart on stale history.
func TestTrendSourceRecordsRepeatPastTheGap(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"flat": {point(7, nil)}}}
	s := NewTrendSource(q, "flat", time.Minute)
	for range 3 {
		if _, err := s.Value(context.Background()); err != nil && err != sharedwidgets.ErrNoData {
			t.Fatalf("Value: %v", err)
		}
	}
	before := len(s.Points())
	s.push(7, time.Now().Add(2*time.Minute))
	if got := len(s.Points()); got != before+1 {
		t.Errorf("points after the gap = %d, want %d", got, before+1)
	}
}

// A changing metric always records, and the buffer stops at the server's cap.
func TestTrendSourceCapsBuffer(t *testing.T) {
	s := NewTrendSource(&stubQuerier{}, "x", time.Minute)
	now := time.Now()
	for i := range trendMaxPoints + 10 {
		s.push(float64(i), now) // distinct values, so the gap never applies
	}
	pts := s.Points()
	if len(pts) != trendMaxPoints {
		t.Fatalf("buffered %d points, want the %d cap", len(pts), trendMaxPoints)
	}
	if pts[len(pts)-1] != float64(trendMaxPoints+9) {
		t.Errorf("newest point = %v, want the latest reading", pts[len(pts)-1])
	}
	if pts[0] != 10 {
		t.Errorf("oldest point = %v, want the buffer to have scrolled", pts[0])
	}
}

// A flat metric must still reach two samples, or the widget never gets created.
func TestTrendSourceBootstrapsThroughTheGap(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"flat": {point(7, nil)}}}
	s := NewTrendSource(q, "flat", time.Hour)
	if _, err := s.Value(context.Background()); err != sharedwidgets.ErrNoData {
		t.Fatalf("first Value err = %v, want ErrNoData", err)
	}
	if _, err := s.Value(context.Background()); err != nil {
		t.Fatalf("second Value on a flat metric must publish, got %v", err)
	}
	if got := s.Points(); len(got) != 2 {
		t.Errorf("Points = %v, want two bootstrap samples despite the gap", got)
	}
}

// A dropped tick must not land in the buffer: an absent series would otherwise
// draw a fake dip in the sparkline.
func TestTrendSourceSkipsNoData(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{}}
	s := &TrendSource{ScalarSource: ScalarSource{Q: q, Expr: "gone"}}
	if _, err := s.Value(context.Background()); err != sharedwidgets.ErrNoData {
		t.Fatalf("Value err = %v, want ErrNoData", err)
	}
	if got := s.Points(); got != nil {
		t.Errorf("Points = %v, want nil after a no-data poll", got)
	}
}

func TestStatRowTimerReachesTheRow(t *testing.T) {
	raw := `[{"slug":"sl","template":"stat_list","stat_rows":[
	  {"label":"Next run","query":"q","value_template":"{{printf \"%.0f\" .Value}}",
	   "timer":{"date":"2026-12-24T18:00:00Z","style":"relative"}}
	]}]`
	cfgs, err := ParseWidgets(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseWidgets: %v", err)
	}
	src, err := NewStatListSource(&stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"q": {point(3, nil)}}}, cfgs[0].StatRows)
	if err != nil {
		t.Fatalf("NewStatListSource: %v", err)
	}
	rows, err := src.Rows(context.Background())
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if rows[0].Timer == nil {
		t.Fatal("row timer is nil; the parsed config timer never reached the row")
	}
	if rows[0].Timer.Style != pushward.TimerStyleRelative {
		t.Errorf("timer style = %q, want relative", rows[0].Timer.Style)
	}
	if rows[0].Value != "3" {
		t.Errorf("row value = %q, want the polled value as the older-client fallback", rows[0].Value)
	}
}

func TestSubtitleTimerReachesContent(t *testing.T) {
	raw := `[{"slug":"a","query":"up","content":{"subtitle":"since boot",
	  "subtitle_timer":{"date":"2026-01-01T00:00:00Z"}}}]`
	cfgs, err := ParseWidgets(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseWidgets: %v", err)
	}
	c := cfgs[0].Content.ToWidgetContent()
	if c.SubtitleTimer == nil {
		t.Fatal("subtitle_timer is nil")
	}
	if c.SubtitleTimer.Style != "" {
		t.Errorf("style = %q, want empty (the server reads that as the default)", c.SubtitleTimer.Style)
	}
	if c.Subtitle != "since boot" {
		t.Errorf("subtitle = %q, want the static fallback preserved", c.Subtitle)
	}
}

func TestScalarSourceNoData(t *testing.T) {
	s := &ScalarSource{Q: &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{}}, Expr: "missing"}
	if _, err := s.Value(context.Background()); err != sharedwidgets.ErrNoData {
		t.Errorf("Value err = %v, want ErrNoData", err)
	}
}

func TestStatListSourceRendersAndFallsBack(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{
		"have": {point(42, nil)},
		// "missing" returns nothing -> the row uses its placeholder.
	}}
	src, err := NewStatListSource(q, []StatRowConfig{
		{Label: "Have", Query: "have", ValueTemplate: `{{printf "%.0f" .Value}}`},
		{Label: "Missing", Query: "missing", ValueTemplate: `{{printf "%.0f" .Value}}`, MissingValue: "n/a"},
	})
	if err != nil {
		t.Fatalf("NewStatListSource: %v", err)
	}
	rows, err := src.Rows(context.Background())
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	want := []pushward.StatRow{{Label: "Have", Value: "42"}, {Label: "Missing", Value: "n/a"}}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// TestStatListSourceTotalOutageReturnsError is the regression guard for the
// all-placeholder bug: when every row's query fails (a datasource-proxy
// outage), Rows must return an error so the manager skips the tick and holds
// the last-good rows rather than publishing a widget of nothing but placeholders.
func TestStatListSourceTotalOutageReturnsError(t *testing.T) {
	boom := errors.New("proxy unreachable")
	q := &stubQuerier{errByExpr: map[string]error{"a": boom, "b": boom}}
	src, err := NewStatListSource(q, []StatRowConfig{
		{Label: "A", Query: "a", ValueTemplate: `{{.Value}}`},
		{Label: "B", Query: "b", ValueTemplate: `{{.Value}}`},
	})
	if err != nil {
		t.Fatalf("NewStatListSource: %v", err)
	}
	if _, err := src.Rows(context.Background()); err == nil {
		t.Fatal("expected an error when every row's query fails, got nil")
	}
}

// TestStatListSourcePartialOutageRendersPlaceholder confirms a single failed row
// falls back to its placeholder while the rest render, and Rows still succeeds.
func TestStatListSourcePartialOutageRendersPlaceholder(t *testing.T) {
	q := &stubQuerier{
		byExpr:    map[string][]bridge.LabeledPoint{"ok": {point(7, nil)}},
		errByExpr: map[string]error{"bad": errors.New("blip")},
	}
	src, err := NewStatListSource(q, []StatRowConfig{
		{Label: "Ok", Query: "ok", ValueTemplate: `{{printf "%.0f" .Value}}`},
		{Label: "Bad", Query: "bad", ValueTemplate: `{{.Value}}`, MissingValue: "n/a"},
	})
	if err != nil {
		t.Fatalf("NewStatListSource: %v", err)
	}
	rows, err := src.Rows(context.Background())
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	want := []pushward.StatRow{{Label: "Ok", Value: "7"}, {Label: "Bad", Value: "n/a"}}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// TestStatListSourceNonFiniteRendersPlaceholder is the regression guard for the
// literal "NaN"/"+Inf" bug: a non-finite reading must render the placeholder.
func TestStatListSourceNonFiniteRendersPlaceholder(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{
		"nan": {point(math.NaN(), nil)},
		"inf": {point(math.Inf(1), nil)},
	}}
	src, err := NewStatListSource(q, []StatRowConfig{
		{Label: "NaN", Query: "nan", ValueTemplate: `{{.Value}}`, MissingValue: "n/a"},
		{Label: "Inf", Query: "inf", ValueTemplate: `{{.Value}}`, MissingValue: "n/a"},
	})
	if err != nil {
		t.Fatalf("NewStatListSource: %v", err)
	}
	rows, err := src.Rows(context.Background())
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	for _, row := range rows {
		if row.Value != "n/a" {
			t.Errorf("row %q value = %q, want the placeholder (non-finite must not render)", row.Label, row.Value)
		}
	}
}

// TestScalarSourceNonFiniteIsNoData confirms the scalar path treats a non-finite
// reading as no data (so the manager skips rather than publishing NaN/Inf).
func TestScalarSourceNonFiniteIsNoData(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"nan": {point(math.NaN(), nil)}}}
	s := &ScalarSource{Q: q, Expr: "nan"}
	if _, err := s.Value(context.Background()); err != sharedwidgets.ErrNoData {
		t.Errorf("Value err = %v, want ErrNoData for a non-finite reading", err)
	}
}
