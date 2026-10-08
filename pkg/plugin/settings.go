package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/mac-lucky/pushward-integrations/shared/e2e"
	"github.com/mac-lucky/pushward-integrations/shared/pushward"

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
	defaultAlsoNotify    = false
	defaultNotifyLevel   = pushward.LevelActive
	defaultHistoryWindow = 30 * time.Minute
	defaultPollInterval  = 30 * time.Second
	defaultCleanupDelay  = 15 * time.Minute
	defaultStaleTimeout  = 24 * time.Hour
	defaultAckRepeat     = 300  // seconds
	defaultAckExpire     = 3600 // seconds
)

// Server bounds for an acknowledged notification, in seconds. Out-of-range
// values are clamped here: the server answers them with a 422 that would cost
// the acknowledge on every alert.
const (
	minAckRepeat = 30
	maxAckRepeat = 3600
	minAckExpire = 60
	maxAckExpire = 10800
)

// Secure-settings keys (DecryptedSecureJSONData).
const (
	secureKeyAPIKey       = "apiKey"
	secureKeyWebhookToken = "webhookToken"
	secureKeyE2EKey       = "e2eKey"
)

// validNotifyLevels is the set of interruption levels the config UI exposes for
// the optional alert push notification (Silent / Normal / Critical). An unknown
// value falls back to defaultNotifyLevel rather than reaching the server, which
// would reject anything outside its own enum.
var validNotifyLevels = map[string]bool{
	pushward.LevelPassive:  true, // Silent
	pushward.LevelActive:   true, // Normal
	pushward.LevelCritical: true, // Critical
}

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

	// AlsoNotify, when true, sends a normal push notification (banner / Lock
	// Screen) alongside the timeline Live Activity when an alert fires and when
	// it resolves. Off by default: the Live Activity alone is the base behavior.
	AlsoNotify bool

	// NotifyLevel is the interruption level for the AlsoNotify push (both the
	// firing and resolved notifications). One of passive (Silent) / active
	// (Normal) / critical (Critical); defaults to active.
	NotifyLevel string

	// Ack makes the firing AlsoNotify push repeat every AckRepeat seconds
	// until it is acknowledged on a device or AckExpire passes. Off by
	// default, and always off at the passive level, which the server refuses
	// to acknowledge.
	Ack       bool
	AckRepeat int
	AckExpire int

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

	// E2EKey seals the AlsoNotify pushes end to end; nil when no key is set.
	// E2EError is non-empty when a key is set but does not parse: the pushes
	// then go out content-free, and the message is surfaced on /config and
	// /healthz like WidgetsError. Only the Key ID of a key is ever shown.
	E2EKey   *e2e.Key
	E2EError string
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
	AlsoNotify      *bool  `json:"alsoNotify"`
	NotifyLevel     string `json:"notifyLevel"`
	AckEnabled      *bool  `json:"ackEnabled"`
	AckRepeat       *int   `json:"ackRepeatSeconds"`
	AckExpire       *int   `json:"ackExpireSeconds"`
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
		AlsoNotify:      defaultAlsoNotify,
		NotifyLevel:     defaultNotifyLevel,
		AckRepeat:       defaultAckRepeat,
		AckExpire:       defaultAckExpire,
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
	if raw.AlsoNotify != nil {
		out.AlsoNotify = *raw.AlsoNotify
	}
	if validNotifyLevels[raw.NotifyLevel] {
		out.NotifyLevel = raw.NotifyLevel
	}
	if raw.AckEnabled != nil {
		out.Ack = *raw.AckEnabled && out.NotifyLevel != pushward.LevelPassive
	}
	// 0 is an emptied number field: keep the default rather than clamping it
	// to the shortest interval.
	if raw.AckRepeat != nil && *raw.AckRepeat > 0 {
		out.AckRepeat = min(max(*raw.AckRepeat, minAckRepeat), maxAckRepeat)
	}
	if raw.AckExpire != nil && *raw.AckExpire > 0 {
		out.AckExpire = min(max(*raw.AckExpire, minAckExpire), maxAckExpire)
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
		if v := s.DecryptedSecureJSONData[secureKeyE2EKey]; strings.TrimSpace(v) != "" {
			if k, err := e2e.ParseKey(v); err != nil {
				out.E2EError = err.Error()
			} else {
				out.E2EKey = k
			}
		}
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
