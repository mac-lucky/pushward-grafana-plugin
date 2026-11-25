package bridge

import (
	"context"
	"math"
	"strconv"
	"time"
)

// ValueDecimals is how many decimal places a reading keeps when it leaves the
// plugin: a widget value, or a timeline value unless the timeline is set to
// show more.
const ValueDecimals = 2

// RoundValue rounds a reading to decimals places for PushWard, keeping at least
// three significant digits so a small reading such as 0.00421 does not collapse
// to 0. A value that is already that short comes back unchanged, and so do 0,
// NaN and the infinities.
func RoundValue(v float64, decimals int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	// 2 - the exponent is how many places three significant digits need.
	decimals = max(decimals, 2-int(math.Floor(math.Log10(math.Abs(v)))))
	r, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', decimals, 64), 64)
	if err != nil {
		return v
	}
	return r
}

// TimelineDecimals is how many decimal places a timeline value keeps: the
// decimals the timeline shows, and ValueDecimals at least, so the chart keeps
// its shape when it shows whole numbers.
func TimelineDecimals(shown int) int {
	return max(shown, ValueDecimals)
}

// RoundingQuerier rounds every value Q returns to Decimals places with
// RoundValue, so a reading leaves as 88.42 rather than 88.42464668357734.
type RoundingQuerier struct {
	Q        MetricsQuerier
	Decimals int
}

// QueryInstantAll implements MetricsQuerier.
func (r RoundingQuerier) QueryInstantAll(ctx context.Context, expr string, ts time.Time) ([]LabeledPoint, error) {
	points, err := r.Q.QueryInstantAll(ctx, expr, ts)
	for i := range points {
		points[i].Point.Value = RoundValue(points[i].Point.Value, r.Decimals)
	}
	return points, err
}

// QueryRangeAll implements MetricsQuerier.
func (r RoundingQuerier) QueryRangeAll(ctx context.Context, expr string, from, to time.Time, step time.Duration) ([]LabeledSeries, error) {
	series, err := r.Q.QueryRangeAll(ctx, expr, from, to, step)
	for _, s := range series {
		for i := range s.Points {
			s.Points[i].Value = RoundValue(s.Points[i].Value, r.Decimals)
		}
	}
	return series, err
}
