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
)

var widgetSlugRE = regexp.MustCompile(`^[a-z0-9_-]{1,128}$`)

// validWidgetTemplates lists the renderers the server supports today.
var validWidgetTemplates = map[string]bool{
	"value": true, "progress": true, "status": true, "gauge": true, "stat_list": true,
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
}

// Triggers reports whether a change in this row's value should drive a PATCH.
func (r StatRowConfig) Triggers() bool { return r.Trigger == nil || *r.Trigger }

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
			return fmt.Errorf("widgets[%d] %q: unknown template %q (allowed: value|progress|status|gauge|stat_list)", i, w.Slug, w.Template)
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
		if w.Template == "stat_list" {
			if len(w.StatRows) == 0 {
				return fmt.Errorf("widgets[%d] %q: template stat_list requires stat_rows (1-%d rows)", i, w.Slug, statListMaxRows)
			}
			if w.Query != "" || w.QueryAll != "" {
				return fmt.Errorf("widgets[%d] %q: template stat_list must not set query or query_all; use per-row queries", i, w.Slug)
			}
		} else {
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
		if (w.Template == "progress" || w.Template == "gauge") && (w.Content.MinValue == nil || w.Content.MaxValue == nil) {
			return fmt.Errorf("widgets[%d] %q: template %q requires content.min_value and content.max_value", i, w.Slug, w.Template)
		}
	}
	return nil
}

func validateStatRows(slug string, idx int, rows []StatRowConfig) error {
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > statListMaxRows {
		return fmt.Errorf("widgets[%d] %q: stat_rows exceeds server cap (%d max, got %d)", idx, slug, statListMaxRows, len(rows))
	}
	for j, row := range rows {
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
