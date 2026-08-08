package widgets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	sharedwidgets "github.com/mac-lucky/pushward-integrations/shared/widgets"

	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/bridge"
)

// Querier is the subset of the plugin's datasource-proxy metrics querier the
// widget sources need. The plugin's *dsQuerier satisfies it, building a fresh
// proxy-authenticated client per call so a rotated Grafana token is always
// honoured by the long-lived poll goroutines.
type Querier interface {
	QueryInstantAll(ctx context.Context, expr string, ts time.Time) ([]bridge.LabeledPoint, error)
}

// firstFinite returns the first result series' value and whether it is finite.
// Matches the bridge's "first result series" scalar semantics, but reports a
// non-finite reading (NaN/Inf, e.g. histogram_quantile over zero observations
// or a divide-by-zero) as absent so callers fall back to ErrNoData or the
// missing-value placeholder rather than rendering the literal "NaN"/"+Inf".
func firstFinite(points []bridge.LabeledPoint) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	v := points[0].Point.Value
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// ScalarSource exposes a single scalar value per call from an instant query.
// Returns shared/widgets.ErrNoData when the query has no result so the manager
// skips the tick rather than treating it as an error.
type ScalarSource struct {
	Q    Querier
	Expr string
}

// Value implements shared/widgets.ValueSource.
func (s *ScalarSource) Value(ctx context.Context) (float64, error) {
	points, err := s.Q.QueryInstantAll(ctx, s.Expr, time.Now())
	if err != nil {
		return 0, err
	}
	v, ok := firstFinite(points)
	if !ok {
		return 0, sharedwidgets.ErrNoData
	}
	return v, nil
}

// Trend sparkline bounds, mirrored from pushward-server: fewer than 2 or more
// than 48 points is rejected.
const (
	trendMinPoints = 2
	trendMaxPoints = 48
)

// TrendSource is a scalar source that also keeps the rolling sample buffer the
// trend template draws as a sparkline. Grafana is queried for one instant value
// per tick; a reading that differs from the last always lands in the buffer,
// and the oldest is dropped once it is full.
//
// Value reports ErrNoData until there are trendMinPoints, which leaves the
// manager's widget create deferred rather than posting a payload the server
// would reject. Both methods run on the manager's one goroutine for this
// widget, so the buffer needs no locking.
type TrendSource struct {
	ScalarSource
	repeatGap  time.Duration
	points     []float64
	lastAppend time.Time
}

// NewTrendSource builds a trend source whose repeat gap is sized off the poll
// interval.
func NewTrendSource(q Querier, expr string, interval time.Duration) *TrendSource {
	return &TrendSource{ScalarSource: ScalarSource{Q: q, Expr: expr}, repeatGap: trendRepeatGap(interval)}
}

// trendRepeatGap is how long an unchanged reading is skipped before being
// recorded again. Mirrors pushward-hass: at the 48-point wire cap, one sample
// per window/48 is the coarsest useful rate, and the buffer's window here is
// interval*48, so the gap is the interval with a 60s floor.
//
// It only bites below a one-minute interval, which is exactly the case that
// needs it: at 5s a flat metric flushes 48 minutes of real history out of the
// buffer in four, leaving a sparkline of 48 identical dots.
func trendRepeatGap(interval time.Duration) time.Duration {
	return max(60*time.Second, interval)
}

// Value implements shared/widgets.ValueSource.
func (s *TrendSource) Value(ctx context.Context) (float64, error) {
	v, err := s.ScalarSource.Value(ctx)
	if err != nil {
		return 0, err
	}
	s.push(v, time.Now())
	if len(s.points) < trendMinPoints {
		return 0, sharedwidgets.ErrNoData
	}
	return v, nil
}

// Points implements shared/widgets.PointSource. The copy keeps the live buffer
// from being aliased into a payload that outlives the tick.
func (s *TrendSource) Points() []float64 {
	if len(s.points) < trendMinPoints {
		return nil
	}
	return slices.Clone(s.points)
}

func (s *TrendSource) push(v float64, now time.Time) {
	n := len(s.points)
	// Below the minimum there is no widget yet, so a repeat still has to count:
	// a metric that starts out flat would otherwise never reach two samples and
	// never publish at all.
	if n >= trendMinPoints && s.points[n-1] == v && now.Sub(s.lastAppend) < s.repeatGap {
		return
	}
	s.lastAppend = now
	if n == trendMaxPoints {
		copy(s.points, s.points[1:])
		s.points[trendMaxPoints-1] = v
		return
	}
	s.points = append(s.points, v)
}

// MultiSource exposes label-keyed fan-out values for queries returning multiple
// series (one widget per series).
type MultiSource struct {
	Q    Querier
	Expr string
}

// Values implements shared/widgets.MultiValueSource.
func (s *MultiSource) Values(ctx context.Context) ([]sharedwidgets.LabeledValue, error) {
	points, err := s.Q.QueryInstantAll(ctx, s.Expr, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]sharedwidgets.LabeledValue, 0, len(points))
	for _, p := range points {
		out = append(out, sharedwidgets.LabeledValue{Labels: p.Labels, Value: p.Point.Value})
	}
	return out, nil
}

// defaultMissingValue is rendered when a stat row's query returns no data and
// no per-row override is configured.
const defaultMissingValue = "—" // em dash, a deliberate iOS display glyph

// NewStatListSource pre-parses every value template so a misconfiguration
// surfaces at construction, not on first tick.
func NewStatListSource(q Querier, rows []StatRowConfig) (sharedwidgets.StatListSource, error) {
	if q == nil {
		return nil, errors.New("stat_list source requires a querier")
	}
	if len(rows) == 0 {
		return nil, errors.New("stat_list source requires at least one row")
	}
	compiled := make([]compiledStatRow, len(rows))
	for i, r := range rows {
		switch {
		case r.Label == "":
			return nil, fmt.Errorf("stat_rows[%d]: label is required", i)
		case r.Query == "":
			return nil, fmt.Errorf("stat_rows[%d]: query is required", i)
		case r.ValueTemplate == "":
			return nil, fmt.Errorf("stat_rows[%d]: value_template is required", i)
		}
		tpl, err := template.New(fmt.Sprintf("row%d", i)).Option("missingkey=zero").Parse(r.ValueTemplate)
		if err != nil {
			return nil, fmt.Errorf("stat_rows[%d]: parsing value_template: %w", i, err)
		}
		missing := r.MissingValue
		if missing == "" {
			missing = defaultMissingValue
		}
		compiled[i] = compiledStatRow{
			label: r.Label, query: r.Query, unit: r.Unit, missing: missing, tpl: tpl, timer: r.timer,
		}
	}
	return &statListSource{q: q, rows: compiled}, nil
}

type compiledStatRow struct {
	label, query, unit, missing string
	tpl                         *template.Template
	timer                       *pushward.TimerValue
}

type statListSource struct {
	q    Querier
	rows []compiledStatRow
}

// Rows fans out the per-row queries concurrently so a stat_list with N rows
// costs roughly one round-trip rather than N. A per-row query error renders that
// row's MissingValue placeholder, so a transient blip on one query never blanks
// the whole widget. But when EVERY row's query fails (a total datasource-proxy
// outage), Rows returns an error so the manager skips the tick and holds the
// last-good rows instead of publishing an all-placeholder widget - matching the
// scalar path, which returns ErrNoData on no data. Capturing now once keeps all
// rows on the same instant.
func (s *statListSource) Rows(ctx context.Context) ([]pushward.StatRow, error) {
	out := make([]pushward.StatRow, len(s.rows))
	errs := make([]error, len(s.rows))
	now := time.Now()
	var wg sync.WaitGroup
	for i, row := range s.rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = s.queryRow(ctx, row, now)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	failed := 0
	var firstErr error
	for _, e := range errs {
		if e != nil {
			failed++
			if firstErr == nil {
				firstErr = e
			}
		}
	}
	if len(s.rows) > 0 && failed == len(s.rows) {
		return nil, fmt.Errorf("all %d stat_list rows failed to query: %w", failed, firstErr)
	}
	return out, nil
}

func (s *statListSource) queryRow(ctx context.Context, row compiledStatRow, now time.Time) (pushward.StatRow, error) {
	points, err := s.q.QueryInstantAll(ctx, row.query, now)
	if err != nil {
		return pushward.StatRow{Label: row.label, Value: row.missing, Unit: row.unit, Timer: row.timer}, err
	}
	rendered := row.missing
	if val, ok := firstFinite(points); ok {
		if v := strings.TrimSpace(renderStatValue(row.tpl, val, row.unit)); v != "" {
			rendered = v
		}
	}
	return pushward.StatRow{Label: row.label, Value: rendered, Unit: row.unit, Timer: row.timer}, nil
}

func renderStatValue(tpl *template.Template, value float64, unit string) string {
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, struct {
		Value float64
		Unit  string
	}{value, unit}); err != nil {
		return ""
	}
	return buf.String()
}
