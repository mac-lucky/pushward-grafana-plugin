package widgets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
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
			label: r.Label, query: r.Query, unit: r.Unit, missing: missing, tpl: tpl,
		}
	}
	return &statListSource{q: q, rows: compiled}, nil
}

type compiledStatRow struct {
	label, query, unit, missing string
	tpl                         *template.Template
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
// last-good rows instead of publishing an all-placeholder widget — matching the
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
		return pushward.StatRow{Label: row.label, Value: row.missing, Unit: row.unit}, err
	}
	rendered := row.missing
	if val, ok := firstFinite(points); ok {
		if v := strings.TrimSpace(renderStatValue(row.tpl, val, row.unit)); v != "" {
			rendered = v
		}
	}
	return pushward.StatRow{Label: row.label, Value: rendered, Unit: row.unit}, nil
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
