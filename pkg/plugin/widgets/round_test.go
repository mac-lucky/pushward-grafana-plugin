package widgets

import (
	"context"
	"math"
	"testing"

	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/bridge"
)

// A widget value goes out rounded: the gauge that showed 88.42464668357734
// on the Widgets page now sends and shows 88.42.
func TestScalarAndTrendSourcesRound(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"long float", 88.42464668357734, 88.42},
		{"integer", 42, 42},
		{"already short", 88.4, 88.4},
		{"very small", 0.0042123456, 0.00421},
		{"very large", 123456789012.3456, 123456789012.35},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"q": {point(tc.in, nil)}}}
			if got, err := (&ScalarSource{Q: q, Expr: "q"}).Value(context.Background()); err != nil || got != tc.want {
				t.Errorf("scalar = (%v, %v), want %v", got, err, tc.want)
			}

			trend := NewTrendSource(q, "q", 0)
			_, _ = trend.Value(context.Background())
			if got, err := trend.Value(context.Background()); err != nil || got != tc.want {
				t.Errorf("trend value = (%v, %v), want %v", got, err, tc.want)
			}
			for _, p := range trend.Points() {
				if p != tc.want {
					t.Errorf("trend point %v, want %v", p, tc.want)
				}
			}
		})
	}
}

// Rounding never carries a reading past a bound it was within: the server
// refuses a gauge value outside min_value..max_value, so 99.996 under a
// max_value of 99.996 must not go out as 100. A reading already outside its
// bounds is only rounded, as before.
func TestRoundingStaysWithinBounds(t *testing.T) {
	lo, hi := 10.004, 99.996
	cases := []struct {
		name     string
		in, want float64
	}{
		{"at the upper bound", 99.996, 99.996},
		{"just under the upper bound", 99.9951, 99.996},
		{"at the lower bound", 10.004, 10.004},
		{"inside", 50.123456, 50.12},
		{"above the upper bound", 100.0049, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"q": {point(tc.in, map[string]string{"instance": "web-1"})}}}
			if got, err := (&ScalarSource{Q: q, Expr: "q", Min: &lo, Max: &hi}).Value(context.Background()); err != nil || got != tc.want {
				t.Errorf("scalar = (%v, %v), want %v", got, err, tc.want)
			}
			got, err := (&MultiSource{Q: q, Expr: "q", Min: &lo, Max: &hi}).Values(context.Background())
			if err != nil || got[0].Value != tc.want {
				t.Errorf("multi = (%+v, %v), want %v", got, err, tc.want)
			}
		})
	}
}

func TestMultiSourceRounds(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"q": {
		point(88.42464668357734, map[string]string{"instance": "web-1"}),
		point(3, map[string]string{"instance": "web-2"}),
		point(math.NaN(), map[string]string{"instance": "web-3"}),
	}}}
	got, err := (&MultiSource{Q: q, Expr: "q"}).Values(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Value != 88.42 || got[1].Value != 3 || got[0].Labels["instance"] != "web-1" {
		t.Errorf("values = %+v, want 88.42 for web-1 and 3 for web-2", got)
	}
	// NaN is passed on as before; the shared manager drops it.
	if !math.IsNaN(got[2].Value) {
		t.Errorf("NaN came back as %v", got[2].Value)
	}
}

// {{.Value}} prints a stat row's reading rounded and in full, while printf and
// clock keep the exact number, so a template that formats on its own sends
// what it sent before.
func TestStatRowValueRounding(t *testing.T) {
	q := &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{
		"cpu":   {point(88.42464668357734, map[string]string{"instance": "web-1"})},
		"users": {point(1234567, nil)},
		"small": {point(0.0042123456, nil)},
		"split": {point(13.496, map[string]string{"window": "five_hour"})},
		"reset": {point(1795000000.6, nil)},
	}}
	cases := []struct {
		name, query, tpl, want string
	}{
		{"long float", "cpu", `{{.Value}}`, "88.42"},
		{"printf keeps the exact number", "cpu", `{{printf "%.3f" .Value}}`, "88.425"},
		{"printf rounds once", "split", `{{printf "%.0f" .Value}}`, "13"},
		{"printf %v prints the rounded number", "cpu", `{{printf "%v%%" .Value}}`, "88.42%"},
		{"large integer in full", "users", `{{.Value}}`, "1234567"},
		{"very small", "small", `{{.Value}}`, "0.00421"},
		{"value_by", "split", `{{.Values.five_hour}}%`, "13.5%"},
		{"comparison", "cpu", `{{if gt .Value 88.42}}hot{{end}}`, "hot"},
		{"clock", "reset", `{{clock .Value "UTC"}}`, clock(1795000000.6, "UTC")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := StatRowConfig{Label: "x", Query: tc.query, ValueTemplate: tc.tpl}
			if tc.query == "split" {
				row.ValueBy = "window"
			}
			src, err := NewStatListSource(q, []StatRowConfig{row})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := src.Rows(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].Value != tc.want {
				t.Errorf("value = %q, want %q", rows[0].Value, tc.want)
			}
		})
	}
}
