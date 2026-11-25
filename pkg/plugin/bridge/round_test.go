package bridge

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

func TestRoundValue(t *testing.T) {
	cases := []struct {
		name     string
		in       float64
		decimals int
		want     float64
	}{
		{"long float", 88.42464668357734, ValueDecimals, 88.42},
		{"negative long float", -88.42464668357734, ValueDecimals, -88.42},
		{"configured decimals", 12.3456789, 4, 12.3457},
		{"fewer decimals than three significant digits", 88.42464668357734, 0, 88.4},
		{"integer", 3, ValueDecimals, 3},
		{"large integer", 1234567, ValueDecimals, 1234567},
		{"already short", 88.4, ValueDecimals, 88.4},
		{"short below one", 0.5, ValueDecimals, 0.5},
		{"below one", 0.123456, ValueDecimals, 0.123},
		{"very small keeps three significant digits", 0.0042123456, ValueDecimals, 0.00421},
		{"tiny", 1.23456e-9, ValueDecimals, 1.23e-9},
		{"very large", 123456789012.3456, ValueDecimals, 123456789012.35},
		{"unix seconds keep their second", 1795000000.123456, ValueDecimals, 1795000000.12},
		{"rounds up across a power of ten", 9.999, ValueDecimals, 10},
		{"zero", 0, ValueDecimals, 0},
		{"largest float", math.MaxFloat64, ValueDecimals, math.MaxFloat64},
		{"smallest float", math.SmallestNonzeroFloat64, ValueDecimals, math.SmallestNonzeroFloat64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RoundValue(tc.in, tc.decimals)
			if got != tc.want {
				t.Errorf("RoundValue(%v, %d) = %v, want %v", tc.in, tc.decimals, got, tc.want)
			}
			if again := RoundValue(got, tc.decimals); again != got {
				t.Errorf("rounding %v again gave %v", got, again)
			}
		})
	}
}

func TestRoundValueLeavesNonFinite(t *testing.T) {
	if got := RoundValue(math.NaN(), ValueDecimals); !math.IsNaN(got) {
		t.Errorf("RoundValue(NaN) = %v, want NaN", got)
	}
	for _, inf := range []float64{math.Inf(1), math.Inf(-1)} {
		if got := RoundValue(inf, ValueDecimals); got != inf {
			t.Errorf("RoundValue(%v) = %v", inf, got)
		}
	}
}

func TestTimelineDecimals(t *testing.T) {
	for shown, want := range map[int]int{0: ValueDecimals, 1: ValueDecimals, ValueDecimals: ValueDecimals, 4: 4} {
		if got := TimelineDecimals(shown); got != want {
			t.Errorf("TimelineDecimals(%d) = %d, want %d", shown, got, want)
		}
	}
}

type longFloatQuerier struct{}

func (longFloatQuerier) QueryRangeAll(context.Context, string, time.Time, time.Time, time.Duration) ([]LabeledSeries, error) {
	return []LabeledSeries{{
		Labels: map[string]string{"instance": "web-1"},
		Points: []pushward.HistoryPoint{{Timestamp: 1, Value: 88.42464668357734}, {Timestamp: 2, Value: 0.0042123456}},
	}}, nil
}

func (longFloatQuerier) QueryInstantAll(context.Context, string, time.Time) ([]LabeledPoint, error) {
	return []LabeledPoint{
		{Labels: map[string]string{"instance": "web-1"}, Point: pushward.HistoryPoint{Timestamp: 1, Value: 88.42464668357734}},
		{Labels: map[string]string{"instance": "web-2"}, Point: pushward.HistoryPoint{Timestamp: 1, Value: 12.3456789}},
	}, nil
}

func TestRoundingQuerier(t *testing.T) {
	q := RoundingQuerier{Q: longFloatQuerier{}, Decimals: 3}

	points, err := q.QueryInstantAll(context.Background(), "up", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := []float64{points[0].Point.Value, points[1].Point.Value}; got[0] != 88.425 || got[1] != 12.346 {
		t.Errorf("instant values = %v, want [88.425 12.346]", got)
	}
	if points[1].Labels["instance"] != "web-2" {
		t.Errorf("labels changed: %v", points[1].Labels)
	}

	series, err := q.QueryRangeAll(context.Background(), "up", time.Now(), time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := series[0].Points; got[0].Value != 88.425 || got[1].Value != 0.00421 || got[1].Timestamp != 2 {
		t.Errorf("range points = %+v, want 88.425 and 0.00421 at their timestamps", got)
	}
}

// NewBridge rounds the readings itself, so a caller that hands it and its
// poller the exact querier still sends polled, history and final values rounded.
func TestNewBridgeRoundsTimelineReadings(t *testing.T) {
	pw := pushward.NewClient("http://127.0.0.1:0", "hlk_x")
	p := NewPoller(longFloatQuerier{}, pw, time.Hour)
	b := NewBridge(pw, pw, longFloatQuerier{}, nil, p, nil, nil, Config{})
	t.Cleanup(b.Stop)

	for name, q := range map[string]MetricsQuerier{"bridge": b.metricsClient, "poller": p.metricsClient} {
		points, err := q.QueryInstantAll(context.Background(), "up", time.Now())
		if err != nil || points[0].Point.Value != 88.42 {
			t.Errorf("%s instant value = %v (%v), want 88.42", name, points[0].Point.Value, err)
		}
		series, err := q.QueryRangeAll(context.Background(), "up", time.Now(), time.Now(), time.Minute)
		if err != nil || series[0].Points[0].Value != 88.42 {
			t.Errorf("%s range value = %v (%v), want 88.42", name, series[0].Points[0].Value, err)
		}
	}
}

// A webhook's values go out rounded like the polled ones: to the decimals the
// timeline shows, ValueDecimals at least.
func TestResolveValuesRounded(t *testing.T) {
	four := 4
	cases := []struct {
		name     string
		decimals *int
		want     float64
	}{
		{"default", nil, 88.42},
		{"timeline shows four", &four, 88.4246},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bridge{cfg: Config{Decimals: tc.decimals}}
			single := b.resolveValues(alert{
				Labels: map[string]string{"alertname": "HighCPU"},
				Values: map[string]float64{"A": 88.42464668357734},
			}, "", "")
			if single["HighCPU"] != tc.want {
				t.Errorf("single value = %v, want %v", single["HighCPU"], tc.want)
			}
			multi := b.resolveValues(alert{
				Labels: map[string]string{"alertname": "HighCPU"},
				Values: map[string]float64{"A": 88.42464668357734, "B": 1},
			}, "", "")
			if multi["A"] != tc.want || multi["B"] != 1 {
				t.Errorf("multi values = %v, want A %v and B 1", multi, tc.want)
			}
		})
	}
}
