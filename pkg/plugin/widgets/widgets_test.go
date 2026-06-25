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
