package widgets

import (
	"fmt"
	"slices"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	sharedwidgets "github.com/mac-lucky/pushward-integrations/shared/widgets"
)

// BuildSpecs converts validated widget configs into shared widget specs,
// attaching the datasource-proxy-backed source (scalar, multi-series, or
// stat_list) for each. Call Validate before BuildSpecs. Returns an error only
// if a stat_list value template fails to compile.
func BuildSpecs(cfgs []WidgetConfig, q Querier) ([]sharedwidgets.Spec, error) {
	specs := make([]sharedwidgets.Spec, 0, len(cfgs))
	for _, w := range cfgs {
		spec := sharedwidgets.Spec{
			Slug:           w.Slug,
			Name:           w.Name,
			Template:       pushward.WidgetTemplate(w.Template),
			Interval:       w.IntervalDuration(),
			UpdateMode:     sharedwidgets.UpdateMode(w.UpdateMode),
			MinChange:      w.MinChange,
			PushThrottle:   w.PushThrottle,
			Content:        w.Content.ToWidgetContent(),
			LabelTemplate:  w.LabelTemplate,
			SlugTemplate:   w.SlugTemplate,
			NameTemplate:   w.NameTemplate,
			MaxSeries:      w.MaxSeries,
			CleanupMissing: w.CleanupMissing,
		}
		switch {
		case w.Template == string(pushward.WidgetTemplateStatList):
			mask := make([]bool, len(w.StatRows))
			for i, r := range w.StatRows {
				mask[i] = r.Triggers()
			}
			src, err := NewStatListSource(q, w.StatRows)
			if err != nil {
				return nil, fmt.Errorf("widget %q: %w", w.Slug, err)
			}
			spec.StatListSource = src
			// Attach the mask only when a row opted out; an all-true mask is
			// behaviorally identical to nil and keeps the fast path.
			if slices.Contains(mask, false) {
				spec.StatChangeMask = mask
			}
		case w.Query != "":
			spec.Source = &ScalarSource{Q: q, Expr: w.Query}
		case w.QueryAll != "":
			spec.MultiSource = &MultiSource{Q: q, Expr: w.QueryAll}
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
