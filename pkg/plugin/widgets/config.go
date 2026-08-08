// Package widgets ports the standalone pushward-grafana bridge's scheduled
// PromQL widget engine into the plugin. It declares the per-widget config the
// operator supplies (via jsonData.widgets), validates it against the server's
// limits, and builds shared/widgets.Spec values whose data sources query the
// Grafana datasource proxy instead of a raw Prometheus endpoint.
//
// The config shape mirrors the bridge's internal/config WidgetConfig so the
// same widget JSON payload the standalone bridge accepts works here verbatim.
// Keep validation in sync with pushward-server's internal/model/widget.go.
package widgets

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	sharedwidgets "github.com/mac-lucky/pushward-integrations/shared/widgets"
)

// Server caps, mirrored from pushward-server/internal/model/widget.go so a
// misconfiguration is rejected at load instead of bouncing off a runtime 422.
// The row cap is sourced from the shared widgets package (its canonical
// "server cap; clients must not exceed" constant) so there is one definition.
const (
	statListMaxRows      = sharedwidgets.DefaultMaxStatRows
	statListLabelMaxRune = 32 // mirror pushward-server/internal/model/widget.go
	statListUnitMaxRune  = 16 // mirror pushward-server/internal/model/widget.go
	expiredTextMaxRune   = 64 // mirror pushward-server/internal/model/widget.go
	staleAfterMin        = 60
	staleAfterMax        = 604800
)

// Widget dates are bounded the same way the server bounds them. Catching a bad
// date here matters more than the duplication: a create the server rejects is
// fatal to Manager.Start, which takes the whole widget engine down with it.
const widgetDateHorizon = 366 * 24 * time.Hour

var widgetDateFloor = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

var widgetSlugRE = regexp.MustCompile(`^[a-z0-9_-]{1,128}$`)

// validWidgetTemplates lists the renderers this plugin can drive from PromQL.
// battery, schedule and flow are server templates the plugin deliberately does
// not offer: each needs several independent readings per push, which the
// one-query-per-widget poll model can't express.
var validWidgetTemplates = map[string]bool{
	"value": true, "progress": true, "status": true, "gauge": true, "stat_list": true,
	"trend": true, "countdown": true,
}

// validTimerStyles allows the empty string because the server reads it as the
// default "timer" style.
var validTimerStyles = map[string]bool{
	"": true, pushward.TimerStyleTimer: true, pushward.TimerStyleRelative: true,
}

// WidgetConfig declares one widget the plugin polls and publishes via the
// pushward-server widget API. Exactly one of Query (scalar), QueryAll
// (multi-series fan-out), or StatRows (stat_list) must be set. The publishing
// key must be an hlk_ integration key with the widgets scope.
type WidgetConfig struct {
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	Template      string  `json:"template"` // value|progress|status|gauge|stat_list; default "value"
	Query         string  `json:"query"`    // PromQL/MetricsQL, scalar variant
	QueryAll      string  `json:"query_all"`
	Interval      string  `json:"interval"`    // Go duration string ("60s"); default 60s, min 5s
	UpdateMode    string  `json:"update_mode"` // "on_change" (default) | "always"
	MinChange     float64 `json:"min_change"`
	PushThrottle  *int    `json:"push_throttle,omitempty"`
	LabelTemplate string  `json:"label_template"`

	// StaleAfter is how many seconds after the last update iOS starts dimming
	// the widget as out of date (60-604800, nil for never). Setting it also
	// arms a heartbeat: the engine re-sends unchanged content every
	// max(30s, stale_after/2) so a flat metric can't age the widget out.
	StaleAfter *int `json:"stale_after,omitempty"`

	// Multi-series fan-out fields.
	SlugTemplate   string `json:"slug_template"`
	NameTemplate   string `json:"name_template"`
	MaxSeries      int    `json:"max_series"`
	CleanupMissing bool   `json:"cleanup_missing"`

	// StatRows is required when Template == "stat_list".
	StatRows []StatRowConfig `json:"stat_rows"`

	Content WidgetContentConfig `json:"content"`

	// interval is the parsed Interval, populated by Validate.
	interval time.Duration
}

// IntervalDuration returns the parsed poll interval (valid only after Validate).
func (w WidgetConfig) IntervalDuration() time.Duration { return w.interval }

// StatRowConfig is one row of a stat_list widget. ValueTemplate is a Go
// template applied to the polled float; vars are .Value (float64) and .Unit
// (string). MissingValue is emitted when the query returns no data.
type StatRowConfig struct {
	Label         string `json:"label"`
	Query         string `json:"query"`
	ValueTemplate string `json:"value_template"`
	Unit          string `json:"unit"`
	MissingValue  string `json:"missing_value"`
	// Trigger controls whether a change in this row's value triggers a widget
	// update; defaults to true (nil -> true). Set false to render the row
	// without letting its value drive PATCHes.
	Trigger *bool `json:"trigger,omitempty"`
	// Timer renders this row's trailing text as a live countdown or count-up
	// instead of the polled value. The value still renders on clients too old
	// to know about timers, so the row keeps its query either way.
	Timer *TimerConfig `json:"timer,omitempty"`

	// timer is the parsed Timer, populated by Validate.
	timer *pushward.TimerValue
}

// Triggers reports whether a change in this row's value should drive a PATCH.
func (r StatRowConfig) Triggers() bool { return r.Trigger == nil || *r.Trigger }

// TimerConfig is a self-updating timer slot: an RFC 3339 anchor plus a style.
// A future date counts down, a past one counts up, and iOS re-renders the text
// on its own - no extra polls and no pushes.
type TimerConfig struct {
	Date  string `json:"date"`
	Style string `json:"style,omitempty"` // "timer" (default) | "relative"
}

// WidgetContentConfig is the static portion of pushward.WidgetContent supplied
// in config. The Value field is filled per-tick from the query.
type WidgetContentConfig struct {
	Icon            string   `json:"icon"`
	Unit            string   `json:"unit"`
	Subtitle        string   `json:"subtitle"`
	Severity        string   `json:"severity"`
	MinValue        *float64 `json:"min_value,omitempty"`
	MaxValue        *float64 `json:"max_value,omitempty"`
	AccentColor     string   `json:"accent_color"`
	BackgroundColor string   `json:"background_color"`
	TextColor       string   `json:"text_color"`

	// StartDate and EndDate are RFC 3339 timestamps driving the countdown
	// template (end_date required) and self-advancing progress bars. ExpiredText
	// replaces the countdown once EndDate passes; without it the widget counts
	// up from EndDate instead.
	StartDate   string `json:"start_date,omitempty"`
	EndDate     string `json:"end_date,omitempty"`
	ExpiredText string `json:"expired_text,omitempty"`

	// SubtitleTimer renders the subtitle as a live timer on any template.
	// Subtitle stays as the static fallback.
	SubtitleTimer *TimerConfig `json:"subtitle_timer,omitempty"`

	// Parsed forms of the date fields above, populated by Validate.
	startDate     *time.Time
	endDate       *time.Time
	subtitleTimer *pushward.TimerValue
}

// ToWidgetContent maps the config shape to the typed pushward content struct.
// Value is left unset; the manager fills it per tick.
func (w WidgetContentConfig) ToWidgetContent() pushward.WidgetContent {
	return pushward.WidgetContent{
		Icon:            w.Icon,
		MinValue:        w.MinValue,
		MaxValue:        w.MaxValue,
		Unit:            w.Unit,
		Subtitle:        w.Subtitle,
		Severity:        w.Severity,
		AccentColor:     w.AccentColor,
		BackgroundColor: w.BackgroundColor,
		TextColor:       w.TextColor,
		StartDate:       w.startDate,
		EndDate:         w.endDate,
		ExpiredText:     w.ExpiredText,
		SubtitleTimer:   w.subtitleTimer,
	}
}

// ParseWidgets decodes the jsonData.widgets array into validated WidgetConfigs.
// Returns (nil, nil) for an empty/absent payload so the engine simply stays
// off. Intervals are Go duration strings; an invalid one fails the whole load
// so a bad widget can't silently never publish.
func ParseWidgets(raw json.RawMessage) ([]WidgetConfig, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var widgets []WidgetConfig
	if err := dec.Decode(&widgets); err != nil {
		return nil, fmt.Errorf("parsing widgets: %w", err)
	}
	if err := Validate(widgets); err != nil {
		return nil, err
	}
	return widgets, nil
}

// Validate normalises defaults and rejects malformed widget configs, parsing
// each interval into the unexported interval field.
func Validate(widgets []WidgetConfig) error {
	seen := make(map[string]int, len(widgets))
	for i := range widgets {
		w := &widgets[i]
		if w.Slug == "" {
			return fmt.Errorf("widgets[%d]: slug is required", i)
		}
		if !widgetSlugRE.MatchString(w.Slug) {
			return fmt.Errorf("widgets[%d] %q: slug must match %s", i, w.Slug, widgetSlugRE)
		}
		if prev, ok := seen[w.Slug]; ok {
			return fmt.Errorf("widgets[%d] %q: duplicate slug (already used by widgets[%d])", i, w.Slug, prev)
		}
		seen[w.Slug] = i
		if w.Name == "" {
			w.Name = w.Slug
		}
		if w.Template == "" {
			w.Template = "value"
		}
		if !validWidgetTemplates[w.Template] {
			return fmt.Errorf("widgets[%d] %q: unknown template %q (allowed: value|progress|status|gauge|stat_list|trend|countdown)", i, w.Slug, w.Template)
		}

		modes := 0
		if w.Query != "" {
			modes++
		}
		if w.QueryAll != "" {
			modes++
		}
		if len(w.StatRows) > 0 {
			modes++
		}
		switch w.Template {
		case "stat_list":
			if len(w.StatRows) == 0 {
				return fmt.Errorf("widgets[%d] %q: template stat_list requires stat_rows (1-%d rows)", i, w.Slug, statListMaxRows)
			}
			if w.Query != "" || w.QueryAll != "" {
				return fmt.Errorf("widgets[%d] %q: template stat_list must not set query or query_all; use per-row queries", i, w.Slug)
			}
		case "countdown":
			// A countdown renders entirely from its own dates on device, so
			// there is nothing to poll and the widget is published once.
			if modes != 0 {
				return fmt.Errorf("widgets[%d] %q: template countdown is static; drop query, query_all and stat_rows", i, w.Slug)
			}
			if w.Content.EndDate == "" {
				return fmt.Errorf("widgets[%d] %q: template countdown requires content.end_date (RFC 3339)", i, w.Slug)
			}
		case "trend":
			// The sparkline is built from the plugin's own rolling buffer of
			// one query's readings, and there is one buffer per widget, so a
			// query_all fan-out has nowhere to keep per-series history.
			if w.Query == "" || modes != 1 {
				return fmt.Errorf("widgets[%d] %q: template trend requires query; query_all fan-out and stat_rows are not supported", i, w.Slug)
			}
		default:
			if modes != 1 || len(w.StatRows) > 0 {
				return fmt.Errorf("widgets[%d] %q: exactly one of query or query_all must be set (stat_rows is only valid with template stat_list)", i, w.Slug)
			}
			if w.QueryAll != "" {
				if w.SlugTemplate == "" {
					return fmt.Errorf("widgets[%d] %q: slug_template is required when query_all is set", i, w.Slug)
				}
				// Mirror shared/widgets.prepare(): the template must reference a
				// label, otherwise every series would collapse onto one slug and
				// the engine would reject the spec at startup (disabling all
				// widgets) instead of at load.
				if !strings.Contains(w.SlugTemplate, "{{") {
					return fmt.Errorf("widgets[%d] %q: slug_template must reference at least one label, e.g. {{.instance}}", i, w.Slug)
				}
			}
		}
		if err := validateStatRows(w.Slug, i, w.StatRows); err != nil {
			return err
		}
		if err := w.validateContent(i); err != nil {
			return err
		}

		d := 60 * time.Second
		if w.Interval != "" {
			parsed, err := time.ParseDuration(w.Interval)
			if err != nil {
				return fmt.Errorf("widgets[%d] %q: invalid interval %q: %w", i, w.Slug, w.Interval, err)
			}
			d = parsed
		}
		if d < 5*time.Second {
			return fmt.Errorf("widgets[%d] %q: interval %v is too short; minimum is 5s", i, w.Slug, d)
		}
		w.interval = d

		if w.UpdateMode == "" {
			w.UpdateMode = "on_change"
		}
		if w.UpdateMode != "on_change" && w.UpdateMode != "always" {
			return fmt.Errorf("widgets[%d] %q: unknown update_mode %q (allowed: on_change|always)", i, w.Slug, w.UpdateMode)
		}
		if err := validateStatListTriggers(i, w); err != nil {
			return err
		}
		// Bounds are required scale for progress/gauge but only optional chart
		// limits for trend, which auto-scales without them.
		if (w.Template == "progress" || w.Template == "gauge") && (w.Content.MinValue == nil || w.Content.MaxValue == nil) {
			return fmt.Errorf("widgets[%d] %q: template %q requires content.min_value and content.max_value", i, w.Slug, w.Template)
		}
		if err := validateStaleAfter(i, w, d); err != nil {
			return err
		}
	}
	return nil
}

// staleAfterIntervalRatio is how many poll intervals must fit inside a
// staleness window. The heartbeat fires at half the window but rides the poll
// ticker, so the worst-case gap between two PATCHes is half the window plus one
// interval. Requiring three intervals caps that at five sixths of the window;
// the obvious-looking two would put it exactly on the boundary and dim the
// widget once every cycle. Fixing it here rather than by shortening the
// heartbeat keeps sharedwidgets.HeartbeatFor free of interval arithmetic and
// puts the error where the operator can act on it.
const staleAfterIntervalRatio = 3

// validateStaleAfter bounds the staleness window and rejects the two shapes
// that would make it misbehave rather than protect: a countdown, which is
// published once and has nothing to refresh, and a window so short relative to
// the poll interval that the widget reads stale between two healthy polls.
func validateStaleAfter(idx int, w *WidgetConfig, interval time.Duration) error {
	if w.StaleAfter == nil {
		return nil
	}
	if w.Template == "countdown" {
		return fmt.Errorf("widgets[%d] %q: template countdown is published once and renders from its own dates, so stale_after has nothing to refresh", idx, w.Slug)
	}
	s := *w.StaleAfter
	if s < staleAfterMin || s > staleAfterMax {
		return fmt.Errorf("widgets[%d] %q: stale_after must be between %d and %d seconds, got %d", idx, w.Slug, staleAfterMin, staleAfterMax, s)
	}
	if floor := staleAfterIntervalRatio * int(interval/time.Second); s < floor {
		return fmt.Errorf("widgets[%d] %q: stale_after %ds is below %d times the %v poll interval (%ds); the heartbeat rides the poll ticker, so the widget would dim before the next refresh landed",
			idx, w.Slug, s, staleAfterIntervalRatio, interval, floor)
	}
	return nil
}

// validateContent parses the date and timer content fields and bounds them the
// way the server does, caching the parsed values for ToWidgetContent.
func (w *WidgetConfig) validateContent(idx int) error {
	c := &w.Content
	maxDate := time.Now().Add(widgetDateHorizon)
	fail := func(err error) error { return fmt.Errorf("widgets[%d] %q: %w", idx, w.Slug, err) }

	var err error
	if c.startDate, err = parseWidgetDate("content.start_date", c.StartDate, maxDate); err != nil {
		return fail(err)
	}
	if c.endDate, err = parseWidgetDate("content.end_date", c.EndDate, maxDate); err != nil {
		return fail(err)
	}
	if c.startDate != nil && c.endDate != nil && !c.startDate.Before(*c.endDate) {
		return fail(errors.New("content.start_date must be before content.end_date"))
	}
	if runeLen(c.ExpiredText) > expiredTextMaxRune {
		return fail(fmt.Errorf("content.expired_text exceeds %d characters", expiredTextMaxRune))
	}
	if c.subtitleTimer, err = parseTimer("content.subtitle_timer", c.SubtitleTimer, maxDate); err != nil {
		return fail(err)
	}
	if c.MinValue != nil && c.MaxValue != nil && *c.MinValue >= *c.MaxValue {
		return fail(fmt.Errorf("content.min_value (%g) must be less than content.max_value (%g)", *c.MinValue, *c.MaxValue))
	}
	return nil
}

// parseWidgetDate parses an optional RFC 3339 timestamp within the server's
// accepted range. The past floor catches the classic milliseconds-for-seconds
// mistake, which lands in 1970.
func parseWidgetDate(field, raw string, maxDate time.Time) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %q is not an RFC 3339 timestamp (e.g. 2026-12-24T18:00:00Z)", field, raw)
	}
	if t.Before(widgetDateFloor) || t.After(maxDate) {
		return nil, fmt.Errorf("%s must fall between %s and %d days from now", field, widgetDateFloor.Format("2006-01-02"), int(widgetDateHorizon/(24*time.Hour)))
	}
	return &t, nil
}

func parseTimer(field string, tc *TimerConfig, maxDate time.Time) (*pushward.TimerValue, error) {
	if tc == nil {
		return nil, nil
	}
	if !validTimerStyles[tc.Style] {
		return nil, fmt.Errorf("%s.style must be one of timer, relative", field)
	}
	d, err := parseWidgetDate(field+".date", tc.Date, maxDate)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("%s.date is required", field)
	}
	return &pushward.TimerValue{Date: *d, Style: tc.Style}, nil
}

func validateStatRows(slug string, idx int, rows []StatRowConfig) error {
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > statListMaxRows {
		return fmt.Errorf("widgets[%d] %q: stat_rows exceeds server cap (%d max, got %d)", idx, slug, statListMaxRows, len(rows))
	}
	maxDate := time.Now().Add(widgetDateHorizon)
	for j := range rows {
		row := &rows[j]
		switch {
		case row.Label == "":
			return fmt.Errorf("widgets[%d] %q: stat_rows[%d].label is required", idx, slug, j)
		case row.Query == "":
			return fmt.Errorf("widgets[%d] %q: stat_rows[%d].query is required", idx, slug, j)
		case row.ValueTemplate == "":
			return fmt.Errorf("widgets[%d] %q: stat_rows[%d].value_template is required", idx, slug, j)
		}
		if runeLen(row.Label) > statListLabelMaxRune {
			return fmt.Errorf("widgets[%d] %q: stat_rows[%d].label exceeds %d characters", idx, slug, j, statListLabelMaxRune)
		}
		if runeLen(row.Unit) > statListUnitMaxRune {
			return fmt.Errorf("widgets[%d] %q: stat_rows[%d].unit exceeds %d characters", idx, slug, j, statListUnitMaxRune)
		}
		timer, err := parseTimer(fmt.Sprintf("stat_rows[%d].timer", j), row.Timer, maxDate)
		if err != nil {
			return fmt.Errorf("widgets[%d] %q: %w", idx, slug, err)
		}
		row.timer = timer
	}
	return nil
}

func runeLen(s string) int { return len([]rune(s)) }

// validateStatListTriggers rejects a stat_list under update_mode on_change where
// every row is trigger:false (it would never PATCH after creation).
func validateStatListTriggers(idx int, w *WidgetConfig) error {
	if w.Template != "stat_list" || w.UpdateMode != "on_change" {
		return nil
	}
	for _, r := range w.StatRows {
		if r.Triggers() {
			return nil
		}
	}
	return fmt.Errorf("widgets[%d] %q: all stat_rows have trigger:false with update_mode on_change; the widget would never update - keep a row as a trigger or set update_mode: always", idx, w.Slug)
}
