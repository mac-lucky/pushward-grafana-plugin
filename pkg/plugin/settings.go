package plugin

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"

	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/widgets"
)

// Default configuration values. These mirror the contract and the standalone
// pushward-grafana bridge defaults so behavior is identical out of the box.
const (
	defaultAPIURL        = "https://api.pushward.app"
	defaultSeverityLabel = "severity"
	defaultSeverity      = "warning"
	defaultPriority      = 5
	defaultScale         = "linear"
	defaultDecimals      = 1
	defaultSmoothing     = true
	defaultHistoryWindow = 30 * time.Minute
	defaultPollInterval  = 30 * time.Second
	defaultCleanupDelay  = 15 * time.Minute
	defaultStaleTimeout  = 24 * time.Hour
)

// Secure-settings keys (DecryptedSecureJSONData).
const (
	secureKeyAPIKey       = "apiKey"
	secureKeyWebhookToken = "webhookToken"
)

// Settings is the parsed plugin configuration: non-secret jsonData merged with
// secret secureJsonData and defaults applied. Durations are parsed from their
// Go-duration string form; invalid values fall back to the default rather than
// failing the whole load, so a single bad field can't dark the bridge.
type Settings struct {
	APIURL          string
	DatasourceUID   string
	SeverityLabel   string
	DefaultSeverity string
	Priority        int
	HistoryWindow   time.Duration
	PollInterval    time.Duration
	CleanupDelay    time.Duration
	StaleTimeout    time.Duration
	Smoothing       bool
	Scale           string
	Decimals        int

	// Widgets are the scheduled-PromQL widget specs published to the server
	// widget API. Empty when no widgets are configured (the engine stays off).
	Widgets []widgets.WidgetConfig
	// WidgetsError holds a non-empty message when the widgets jsonData failed
	// to parse/validate. The engine stays off and the message is surfaced on
	// /config and /healthz, but the timeline path is unaffected: one bad
	// widget config must not dark the whole plugin.
	WidgetsError string

	// Secrets — never echoed by /config or logged.
	APIKey       string
	WebhookToken string
}

// rawJSONData is the on-the-wire shape of jsonData. Numbers and booleans use
// pointers so an absent key is distinguishable from a zero value and the
// default applies.
type rawJSONData struct {
	APIURL          string `json:"apiUrl"`
	DatasourceUID   string `json:"datasourceUid"`
	SeverityLabel   string `json:"severityLabel"`
	DefaultSeverity string `json:"defaultSeverity"`
	Priority        *int   `json:"priority"`
	HistoryWindow   string `json:"historyWindow"`
	PollInterval    string `json:"pollInterval"`
	CleanupDelay    string `json:"cleanupDelay"`
	StaleTimeout    string `json:"staleTimeout"`
	Smoothing       *bool  `json:"smoothing"`
	Scale           string `json:"scale"`
	Decimals        *int   `json:"decimals"`
	// Widgets is the raw widget array, parsed + validated by the widgets
	// package. Kept as RawMessage so a malformed entry yields a precise
	// per-widget error instead of failing the whole jsonData unmarshal.
	Widgets json.RawMessage `json:"widgets"`
}

// LoadSettings parses an AppInstanceSettings into a Settings with all defaults
// applied. It returns an error only when jsonData is present but not valid JSON.
func LoadSettings(s backend.AppInstanceSettings) (*Settings, error) {
	var raw rawJSONData
	if len(s.JSONData) > 0 {
		if err := json.Unmarshal(s.JSONData, &raw); err != nil {
			return nil, fmt.Errorf("parsing plugin jsonData: %w", err)
		}
	}

	out := &Settings{
		APIURL:          firstNonEmpty(raw.APIURL, defaultAPIURL),
		DatasourceUID:   raw.DatasourceUID,
		SeverityLabel:   firstNonEmpty(raw.SeverityLabel, defaultSeverityLabel),
		DefaultSeverity: firstNonEmpty(raw.DefaultSeverity, defaultSeverity),
		Priority:        defaultPriority,
		HistoryWindow:   parseDurationOr(raw.HistoryWindow, defaultHistoryWindow),
		PollInterval:    parseDurationOr(raw.PollInterval, defaultPollInterval),
		CleanupDelay:    parseDurationOr(raw.CleanupDelay, defaultCleanupDelay),
		StaleTimeout:    parseDurationOr(raw.StaleTimeout, defaultStaleTimeout),
		Smoothing:       defaultSmoothing,
		Scale:           firstNonEmpty(raw.Scale, defaultScale),
		Decimals:        defaultDecimals,
	}
	if raw.Priority != nil {
		out.Priority = *raw.Priority
	}
	if raw.Smoothing != nil {
		out.Smoothing = *raw.Smoothing
	}
	if raw.Decimals != nil {
		out.Decimals = *raw.Decimals
	}

	// Parse widgets out of band: a malformed entry must not fail the whole
	// settings load (which would dark the timeline path too). On error the
	// engine stays off and the message is surfaced to the UI.
	if w, werr := widgets.ParseWidgets(raw.Widgets); werr != nil {
		out.WidgetsError = werr.Error()
	} else {
		out.Widgets = w
	}

	if s.DecryptedSecureJSONData != nil {
		out.APIKey = s.DecryptedSecureJSONData[secureKeyAPIKey]
		out.WebhookToken = s.DecryptedSecureJSONData[secureKeyWebhookToken]
	}

	return out, nil
}

func firstNonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// parseDurationOr parses a Go-duration string, returning fallback on an empty
// or invalid value so one malformed field cannot break the whole config.
func parseDurationOr(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
