package bridge

import (
	"strings"
	"testing"
)

// TestBuildContentPrimarySeries covers the pushward_primary_series annotation:
// it names the timeline series that drives the iOS headline, is left empty when
// absent (server falls back to the alphabetically first series), and is dropped
// past the server's 32-rune cap so a stray value can't reject the whole update.
func TestBuildContentPrimarySeries(t *testing.T) {
	b := &Bridge{cfg: Config{}}
	values := map[string]float64{"cpu": 1, "mem": 2}

	got := b.buildContent(alert{Annotations: map[string]string{"pushward_primary_series": "mem"}}, "warning", values)
	if got.PrimarySeries != "mem" {
		t.Errorf("expected PrimarySeries=mem, got %q", got.PrimarySeries)
	}

	got = b.buildContent(alert{Annotations: map[string]string{}}, "warning", values)
	if got.PrimarySeries != "" {
		t.Errorf("expected empty PrimarySeries when unset, got %q", got.PrimarySeries)
	}

	oversized := strings.Repeat("x", 33)
	got = b.buildContent(alert{Annotations: map[string]string{"pushward_primary_series": oversized}}, "warning", values)
	if got.PrimarySeries != "" {
		t.Errorf("expected oversized PrimarySeries to be skipped, got %q", got.PrimarySeries)
	}
}
