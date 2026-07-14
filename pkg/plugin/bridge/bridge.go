// Package bridge is the embedded Grafana→PushWard timeline bridge. It is ported
// from pushward-integrations/grafana/internal/{handler,metrics,poller}: a
// Grafana alerting webhook loops back into the plugin backend, the bridge
// resolves the alert's PromQL, queries history through the Grafana datasource
// proxy, builds a timeline Live Activity Content, and drives its lifecycle
// (create → ongoing updates via the poller → ended on resolve) against
// api.pushward.app using the shared/pushward wire contract.
package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	"github.com/mac-lucky/pushward-integrations/shared/syncx"
	"github.com/mac-lucky/pushward-integrations/shared/text"
)

const (
	alertStatusFiring   = "firing"
	alertStatusResolved = "resolved"

	templateTimeline   = pushward.TemplateTimeline
	defaultWarningIcon = "exclamationmark.triangle.fill"
	resolvedIcon       = "checkmark.circle.fill"

	activeAlertCap = 500 // max concurrent tracked alerts; new entries are dropped when full

	// Alert annotation keys the bridge reads to shape the timeline.
	annPrimarySeries = "pushward_primary_series"
	annUnit          = "pushward_unit"
	annSummary       = "summary"

	// Length caps for the optional "also send a notification" push. Subtitle/body
	// mirror the standalone relay's Grafana notification shape; the title cap
	// matches the server limit (notification_handler.go). Truncating the title is
	// defensive: alertname is <=256 today only because CreateActivity already
	// capped it, but that invariant shouldn't be load-bearing here.
	maxNotifyTitleRunes    = 256
	maxNotifySubtitleRunes = 80
	maxNotifyBodyRunes     = 120

	// webhookTimeout bounds slow Prometheus/PushWard calls for a single async
	// webhook so they can be interrupted rather than blocking indefinitely.
	webhookTimeout = 30 * time.Second

	// alertCheckInterval bounds how often the alertmanager backstop runs. It is
	// a safety net behind the webhook self-loop, so it stays conservative to
	// avoid hammering the alertmanager API even with many active alerts.
	alertCheckInterval = time.Minute
)

// GrafanaResolver resolves a Grafana alert rule's PromQL and reports firing
// state. Implemented by grafanaapi.Client; kept as an interface here so the
// bridge has no dependency on the concrete Grafana API client and is testable
// with a stub. nil disables auto-extract and the alertmanager backstop.
type GrafanaResolver interface {
	ExtractRuleUID(generatorURL string) string
	GetRuleQuery(ctx context.Context, ruleUID string) (expr, refID string, err error)
	IsAlertFiring(ctx context.Context, alertname string) (bool, error)
}

// DeliveryLogger records a delivery outcome for the /history surface. Optional;
// a nil logger disables history recording.
type DeliveryLogger interface {
	Log(alertname, slug, action string, ok bool, detail string)
}

// Metrics counts bridge activity for the Prometheus counters. Optional; a nil
// Metrics disables counting.
type Metrics interface {
	IncAlertsReceived(n int)
	IncActivitiesCreated()
	IncPushesSent()
	IncErrors()
}

// Config holds timeline display and lifecycle settings for the bridge.
type Config struct {
	HistoryWindow   time.Duration
	Priority        int
	CleanupDelay    time.Duration
	StaleTimeout    time.Duration
	SeverityLabel   string
	DefaultSeverity string
	Smoothing       *bool
	Scale           string
	Decimals        *int
	// AlsoNotify sends a normal push notification (banner / Lock Screen)
	// alongside the timeline Live Activity: one when an alert starts firing and
	// one when it resolves. Off by default.
	AlsoNotify bool
}

// Bridge receives Grafana webhook alert notifications and creates PushWard
// timeline activities with sparkline history.
type Bridge struct {
	pwClient      *pushward.Client
	metricsClient MetricsQuerier
	grafanaClient GrafanaResolver // nil if auto-extract disabled
	poller        *Poller
	deliveryLog   DeliveryLogger // nil if history recording disabled
	metrics       Metrics        // nil if metric counting disabled
	cfg           Config

	mu       sync.Mutex
	active   map[string]*alertState
	wg       sync.WaitGroup // tracks in-flight async webhook processing
	bgWg     sync.WaitGroup // tracks sweeper/checker background goroutines
	capDrops *syncx.DropCounter

	stop     context.CancelFunc // cancels the sweeper/checker background context
	stopOnce sync.Once
}

type alertState struct {
	slug string
	// alertname is the real "alertname" label ("" for anonymous alerts whose
	// map key is the fingerprint). The alert-state checker matches on this, not
	// the map key, so it can't query a fingerprint as an alertname.
	alertname string
	// ruleUID is the Grafana alert-rule UID extracted from the alert's
	// generatorURL (empty when it can't be parsed). It's the preferred silence
	// matcher (__alert_rule_uid__) for the management UI's "Silence" action.
	ruleUID     string
	expr        string
	refID       string
	seriesLabel string
	// primary is the pushward_primary_series annotation captured at create time.
	// Stored so the terminal ENDED update (incl. the annotation-free alertmanager
	// backstop) caps to the same headline series the poller kept, instead of
	// pruning it as an orphan on the last frame.
	primary      string
	lastSeen     time.Time
	fingerprints map[string]struct{}
	// lastValues records the series keys + values most recently sent to the
	// server. Reused on resolve to keep keys stable across the firing→end
	// transition so the server's AccumulateHistory preserves accumulated
	// history instead of pruning it as an orphaned series.
	lastValues map[string]float64
}

// NewBridge constructs the bridge and starts its background sweeper and
// alertmanager backstop. Call Stop to tear them down. p is the per-alert poller
// (constructed by the caller with the same metrics querier); gr may be nil to
// disable PromQL auto-extract and the alertmanager backstop; dl and m may be nil.
func NewBridge(
	pw *pushward.Client,
	mq MetricsQuerier,
	gr GrafanaResolver,
	p *Poller,
	dl DeliveryLogger,
	m Metrics,
	cfg Config,
) *Bridge {
	if cfg.SeverityLabel == "" {
		cfg.SeverityLabel = "severity"
	}
	if cfg.DefaultSeverity == "" {
		cfg.DefaultSeverity = "warning"
	}
	b := &Bridge{
		pwClient:      pw,
		metricsClient: mq,
		grafanaClient: gr,
		poller:        p,
		deliveryLog:   dl,
		metrics:       m,
		cfg:           cfg,
		active:        make(map[string]*alertState),
		capDrops:      syncx.NewDropCounter(100),
	}
	if p != nil {
		p.SetUpdateCallback(b.recordPollerValues)
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.stop = cancel
	b.startSweeper(ctx, cfg.StaleTimeout)
	b.startAlertChecker(ctx, alertCheckInterval)
	return b
}

// Stop cancels the background sweeper/checker, stops all pollers, and waits for
// in-flight webhook processing and background goroutines to finish. Safe to call
// multiple times.
func (b *Bridge) Stop() {
	b.stopOnce.Do(func() {
		if b.stop != nil {
			b.stop()
		}
		// Drain in-flight webhook goroutines (producers) BEFORE stopping the
		// pollers (consumers): a webhook still in flight can call poller.Start,
		// and a Start that wins the race after StopAll would insert a cancel
		// that nothing invokes again, leaving Wait blocked forever.
		b.wg.Wait()
		if b.poller != nil {
			b.poller.StopAll()
			b.poller.Wait()
		}
		b.bgWg.Wait()
	})
}

// ActiveCount returns the number of currently tracked alerts.
func (b *Bridge) ActiveCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.active)
}

// recordPollerValues is invoked by the poller after each successful poll. It
// stores the values under the matching alertState so they can be reused on
// resolve to preserve accumulated history.
func (b *Bridge) recordPollerValues(slug string, values map[string]float64) {
	if len(values) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, state := range b.active {
		if state.slug == slug {
			state.lastValues = values
			return
		}
	}
}

type webhookPayload struct {
	Status string  `json:"status"`
	Alerts []alert `json:"alerts"`
}

type alert struct {
	Status       string             `json:"status"`
	Labels       map[string]string  `json:"labels"`
	Annotations  map[string]string  `json:"annotations"`
	Values       map[string]float64 `json:"values"`
	StartsAt     string             `json:"startsAt"`
	Fingerprint  string             `json:"fingerprint"`
	GeneratorURL string             `json:"generatorURL"`
}

// ProcessWebhook parses a Grafana alerting webhook payload and processes its
// firing/resolved alerts asynchronously. It returns quickly: parse errors are
// returned for logging (the resource handler still answers 200 so Grafana does
// not retry), and valid payloads are handed to a background goroutine bounded by
// webhookTimeout.
func (b *Bridge) ProcessWebhook(_ context.Context, rawJSON []byte) error {
	var payload webhookPayload
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return err
	}

	if b.metrics != nil {
		b.metrics.IncAlertsReceived(len(payload.Alerts))
	}

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		// Use a bounded background context so slow Prometheus/PushWard calls can
		// still be interrupted on shutdown rather than blocking indefinitely.
		ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
		defer cancel()
		for _, a := range payload.Alerts {
			switch a.Status {
			case alertStatusFiring:
				b.handleFiring(ctx, a)
			case alertStatusResolved:
				b.handleResolved(ctx, a)
			}
		}
	}()
	return nil
}

func (b *Bridge) handleFiring(ctx context.Context, a alert) {
	if a.Fingerprint == "" {
		slog.Warn("alert has empty fingerprint, skipping")
		return
	}

	alertname := a.Labels["alertname"]
	// When alertname is empty, use the fingerprint as the map key to avoid
	// collapsing unrelated anonymous alerts into a single activity.
	mapKey := alertname
	if mapKey == "" {
		mapKey = a.Fingerprint
		alertname = "Grafana Alert"
	}
	slug := makeSlug(mapKey)
	logger := slog.With("alertname", alertname, "slug", slug, "fingerprint", a.Fingerprint)

	seriesLabel := a.Annotations["pushward_series_label"]

	// Check-and-mark: reserve the slot before releasing the lock to prevent
	// duplicate creates from concurrent webhooks.
	b.mu.Lock()
	existing := b.active[mapKey]
	isNew := existing == nil
	if isNew {
		if len(b.active) >= activeAlertCap {
			b.mu.Unlock()
			if n, shouldLog := b.capDrops.Drop(); shouldLog {
				logger.Warn("active alert cap reached, dropping firing alert",
					"cap", activeAlertCap, "total_dropped", n)
			}
			return
		}
		b.active[mapKey] = &alertState{
			slug: slug, alertname: a.Labels["alertname"], ruleUID: b.ruleUIDFor(a),
			seriesLabel: seriesLabel, primary: a.Annotations[annPrimarySeries],
			lastSeen:     time.Now(),
			fingerprints: map[string]struct{}{a.Fingerprint: {}},
		}
	} else {
		existing.lastSeen = time.Now()
		existing.fingerprints[a.Fingerprint] = struct{}{}
	}
	b.mu.Unlock()

	expr, refID := b.resolveQuery(ctx, a)

	if isNew {
		err := b.pwClient.CreateActivity(ctx, slug, alertname, b.cfg.Priority,
			int(b.cfg.CleanupDelay.Seconds()), int(b.cfg.StaleTimeout.Seconds()))
		if err != nil {
			b.mu.Lock()
			delete(b.active, mapKey)
			b.mu.Unlock()
			logger.Error("failed to create activity", "error", err)
			b.recordError()
			b.record(alertname, slug, "create", false, err.Error())
			return
		}
		logger.Info("activity created")
		b.recordActivityCreated()
		b.record(alertname, slug, "created", true, "")

		// Re-check entry exists before writing — a concurrent resolved webhook
		// could have deleted the entry between the unlock above and this lock.
		b.mu.Lock()
		if state, ok := b.active[mapKey]; ok {
			state.expr = expr
			state.refID = refID
		}
		b.mu.Unlock()
	}

	severity := b.resolveSeverity(a)

	// Optionally fire a normal push notification once per new alert. Deferred so
	// it runs after the timeline create/update rather than ahead of it: the push
	// call retries internally under the shared webhook deadline, so sending it
	// inline would delay - and on a degraded /notifications endpoint could cancel
	// - the core Live Activity update. Deferring also covers the no-values early
	// return, so the user is still notified when the alert carries no values yet.
	if isNew && b.cfg.AlsoNotify {
		defer b.sendAlertNotification(ctx, logger, a, slug, alertname, false)
	}

	// For new alerts, fetch history first so we can derive current values with
	// proper metric labels instead of Grafana expression ref IDs (B, C).
	var history map[string][]pushward.HistoryPoint
	if isNew && expr != "" {
		history = b.fetchHistoryAll(ctx, logger, expr, seriesLabel)
	}

	var values map[string]float64
	if len(history) > 0 {
		values = latestValues(history)
	} else {
		values = b.resolveValues(a, refID, seriesLabel)
	}

	if len(values) == 0 {
		logger.Warn("no values available, skipping initial update (poller will seed and populate)",
			"expr_resolved", expr != "", "refID", refID)
		if isNew && expr != "" {
			// Hand the timeline template/styling to the poller so it seeds the
			// activity on its first tick that yields values — otherwise the
			// activity would be left on the generic template while ONGOING. (A
			// timeline seed with empty values would be rejected 422, so we can't
			// seed here.)
			seed := b.buildContent(a, severity, nil)
			if b.startPollerIfTracked(mapKey, slug, expr, seriesLabel, a.Annotations[annPrimarySeries], &seed) {
				logger.Info("poller started (will seed on first values)")
			}
		}
		return
	}

	primary := a.Annotations[annPrimarySeries]
	if capped, trimmed := capSeries(values, primary); trimmed {
		logger.Warn("timeline series capped to server limit",
			"limit", maxTimelineSeries, "dropped", len(values)-len(capped))
		values = capped
		history = capHistory(history, values)
	}

	content := b.buildContent(a, severity, values)
	content.History = history

	err := b.pwClient.UpdateActivity(ctx, slug, pushward.UpdateRequest{
		State:   pushward.StateOngoing,
		Content: content,
	})
	if err != nil {
		logger.Error("failed to update activity", "error", err)
		b.recordError()
		b.record(alertname, slug, "firing", false, err.Error())
		return
	}
	b.recordPushSent()
	b.record(alertname, slug, "firing", true, content.State)

	// Only seed lastValues from the initial history fetch — values from the
	// webhook payload use Grafana ref-ID keys that differ from the poller's
	// metric-derived keys. Letting a re-fire overwrite would drop accumulated
	// history on resolve.
	if len(history) > 0 {
		b.mu.Lock()
		if state, ok := b.active[mapKey]; ok {
			state.lastValues = values
		}
		b.mu.Unlock()
	}

	if isNew && expr != "" {
		if b.startPollerIfTracked(mapKey, slug, expr, seriesLabel, primary, nil) {
			logger.Info("poller started")
		}
	}
}

// startPollerIfTracked starts the per-alert poller for slug only while the alert
// is still tracked, holding b.mu across the presence check and the Start. A
// concurrent resolved webhook deletes the entry under b.mu and then calls
// poller.Stop without it, so holding b.mu here means the two interleave cleanly:
// either Start runs first (the poller lands in p.active and the later Stop
// cancels it) or the entry is already gone (we skip Start). That closes the
// window where a resolved racing an in-flight firing-create could leave a poller
// running forever against an already-ended activity. poller.Start/StartWithSeed
// take only p.mu, never b.mu, so there is no lock-ordering cycle. Returns whether
// a poller was started.
func (b *Bridge) startPollerIfTracked(mapKey, slug, expr, seriesLabel, primary string, seed *pushward.Content) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.active[mapKey]; !ok {
		return false // resolved or swept while this firing was still in flight
	}
	b.poller.StartWithSeed(slug, expr, seriesLabel, primary, seed)
	return true
}

func (b *Bridge) handleResolved(ctx context.Context, a alert) {
	if a.Fingerprint == "" {
		slog.Warn("alert has empty fingerprint, skipping")
		return
	}

	alertname := a.Labels["alertname"]
	mapKey := alertname
	if mapKey == "" {
		mapKey = a.Fingerprint
		alertname = "Grafana Alert"
	}
	slug := makeSlug(mapKey)
	logger := slog.With("alertname", alertname, "slug", slug, "fingerprint", a.Fingerprint)

	b.mu.Lock()
	state, exists := b.active[mapKey]
	if !exists {
		b.mu.Unlock()
		return
	}

	delete(state.fingerprints, a.Fingerprint)

	if len(state.fingerprints) > 0 {
		remaining := len(state.fingerprints)
		b.mu.Unlock()
		logger.Info("instance resolved, other instances still firing", "remaining", remaining)
		return
	}

	// All instances resolved — capture state and clean up.
	refID := state.refID
	seriesLabel := state.seriesLabel
	expr := state.expr
	lastValues := state.lastValues
	primary := state.primary
	delete(b.active, mapKey)
	b.mu.Unlock()

	b.poller.Stop(slug)

	severity := b.resolveSeverity(a)

	// Reuse the same series keys the poller was sending so the server's
	// AccumulateHistory keeps the accumulated history instead of pruning it as
	// an orphan series on the final update.
	values := b.finalValues(ctx, expr, seriesLabel, lastValues)
	if len(values) == 0 {
		values = b.resolveValues(a, refID, seriesLabel)
	}
	values, _ = capSeries(values, primary)

	content := b.buildContent(a, severity, values)
	content.Icon = resolvedIcon
	content.AccentColor = pushward.ColorGreen

	if err := b.pwClient.UpdateActivity(ctx, slug, pushward.UpdateRequest{
		State:   pushward.StateEnded,
		Content: content,
	}); err != nil {
		logger.Error("failed to end activity", "error", err)
		b.recordError()
		b.record(alertname, slug, "resolved", false, err.Error())
	} else {
		logger.Info("activity ended")
		b.recordPushSent()
		b.record(alertname, slug, "resolved", true, "")
	}

	// Fire the passive "resolved" push regardless of the ended-update outcome: if
	// that update failed the activity is still live on-device, which is exactly
	// when the user most needs the resolved notification.
	if b.cfg.AlsoNotify {
		b.sendAlertNotification(ctx, logger, a, slug, alertname, true)
	}
}

// finalValues returns the values to send on the terminal ENDED update, keyed by
// the same series keys the poller was using so the server preserves history.
// Prefers a fresh instant query; falls back to the last values recorded from the
// poller / firing update.
func (b *Bridge) finalValues(ctx context.Context, expr, seriesLabel string, lastValues map[string]float64) map[string]float64 {
	if expr != "" && b.metricsClient != nil {
		points, err := b.metricsClient.QueryInstantAll(ctx, expr, time.Now())
		if err == nil && len(points) > 0 {
			values := make(map[string]float64, len(points))
			for _, lp := range points {
				key := SeriesKey(lp.Labels, seriesLabel)
				values[key] = lp.Point.Value
			}
			return values
		}
	}
	return lastValues
}

func (b *Bridge) resolveQuery(ctx context.Context, a alert) (expr, refID string) {
	if q, ok := a.Annotations["pushward_query"]; ok && q != "" {
		refID = a.Annotations["pushward_ref_id"]
		return q, refID
	}

	if b.grafanaClient != nil {
		ruleUID := b.grafanaClient.ExtractRuleUID(a.GeneratorURL)
		if ruleUID != "" {
			rqExpr, rqRefID, err := b.grafanaClient.GetRuleQuery(ctx, ruleUID)
			if err != nil {
				slog.Warn("failed to auto-extract query", "rule_uid", ruleUID, "error", err)
			} else {
				return rqExpr, rqRefID
			}
		}
	}

	return "", ""
}

func (b *Bridge) resolveSeverity(a alert) string {
	if sev, ok := a.Labels[b.cfg.SeverityLabel]; ok {
		switch sev {
		case pushward.SeverityCritical, pushward.SeverityWarning, pushward.SeverityInfo:
			return sev
		}
	}
	return b.cfg.DefaultSeverity
}

// resolveValues builds a multi-key value map from the webhook payload. For
// single-series alerts it returns a single-key map; for multi-series it returns
// N keys.
func (b *Bridge) resolveValues(a alert, preferredRefID, seriesLabel string) map[string]float64 {
	if len(a.Values) == 0 {
		return nil
	}

	if rid := a.Annotations["pushward_ref_id"]; rid != "" {
		preferredRefID = rid
	}

	label := a.Labels["alertname"]
	if label == "" {
		label = "Value"
	}
	// label becomes a timeline value-map key; the server rejects keys over
	// maxSeriesKeyRunes, so cap it as SeriesKey does for metric-derived keys.
	label = text.TruncateHard(label, maxSeriesKeyRunes)

	// Single ref ID match — use alertname as key for backward compatibility.
	if preferredRefID != "" {
		if v, ok := a.Values[preferredRefID]; ok {
			return map[string]float64{label: v}
		}
	}

	// Single value — use alertname as key.
	if len(a.Values) == 1 {
		for _, v := range a.Values {
			return map[string]float64{label: v}
		}
	}

	// Multi-value: use the webhook's ref ID keys directly. These are Grafana
	// expression ref IDs (A, B, C...), not ideal series labels, but the poller
	// will replace them with proper metric-derived keys on next tick.
	result := make(map[string]float64, len(a.Values))
	for k, v := range a.Values {
		result[k] = v
	}
	return result
}

func latestValues(history map[string][]pushward.HistoryPoint) map[string]float64 {
	values := make(map[string]float64, len(history))
	for key, points := range history {
		if len(points) > 0 {
			values[key] = points[len(points)-1].Value
		}
	}
	return values
}

func (b *Bridge) buildContent(a alert, severity string, values map[string]float64) pushward.Content {
	var value any
	if len(values) > 0 {
		value = values
	}

	content := pushward.Content{
		Template:    templateTimeline,
		Value:       value,
		Subtitle:    "Grafana",
		AccentColor: pushward.SeverityColor(severity),
		Icon:        pushward.SeverityIcon(severity, defaultWarningIcon),
		Scale:       b.cfg.Scale,
		Smoothing:   b.cfg.Smoothing,
		Decimals:    b.cfg.Decimals,
	}

	if v, ok := a.Annotations[annUnit]; ok {
		content.Unit = text.TruncateHard(v, maxUnitRunes)
	}

	if v, ok := a.Annotations["pushward_threshold"]; ok {
		if f, ok := parseAnnotationThreshold(v); ok {
			content.Thresholds = []pushward.Threshold{{
				Value: f,
				Color: pushward.SeverityColor(severity),
			}}
		}
	}

	if summary, ok := a.Annotations[annSummary]; ok {
		content.State = text.Truncate(summary, maxStateRunes)
	} else {
		content.State = text.Truncate(a.Labels["alertname"], maxStateRunes)
	}

	// A multi-series timeline defaults its headline to the alphabetically first
	// series; an alert annotation can name the series that should drive the
	// headline number and the high/low range instead. Set it whenever present (a
	// single-series timeline ignores it) so it also rides the poller-seeded path
	// where the firing webhook carried no values. Skipped past the server's
	// 32-rune cap so a stray value can't reject the whole update.
	if v := a.Annotations[annPrimarySeries]; v != "" && len([]rune(v)) <= maxSeriesKeyRunes {
		content.PrimarySeries = v
	}

	return content
}

// buildAlertNotification builds the optional normal push notification for an
// alert, mirroring the standalone relay's Grafana notification shape so the two
// PushWard-Grafana paths look identical on device. Firing notifications are
// active (they alert the user); resolved notifications are passive. The
// CollapseID keys per alertname so a firing push is replaced by its resolved
// push rather than stacking.
func buildAlertNotification(a alert, alertname string, resolved bool) pushward.SendNotificationRequest {
	subtitle := "Grafana"
	if instance := a.Labels["instance"]; instance != "" {
		subtitle = text.Truncate("Grafana · "+instance, maxNotifySubtitleRunes)
	}

	req := pushward.SendNotificationRequest{
		Title:      text.Truncate(alertname, maxNotifyTitleRunes),
		Subtitle:   subtitle,
		ThreadID:   "grafana",
		CollapseID: text.SlugHash("grafana", alertname, 6),
		Source:     "grafana",
		Push:       true,
	}

	summary := a.Annotations[annSummary]
	if resolved {
		req.Level = pushward.LevelPassive
		if summary != "" {
			req.Body = text.Truncate("Resolved · "+summary, maxNotifyBodyRunes)
		} else {
			req.Body = "Resolved"
		}
	} else {
		req.Level = pushward.LevelActive
		if summary != "" {
			req.Body = text.Truncate(summary, maxNotifyBodyRunes)
		} else {
			// The server rejects an empty body (minLength 1), so fall back to the
			// alert name for a summary-less alert. alertname is never empty (the
			// caller substitutes "Grafana Alert" for anonymous alerts).
			req.Body = alertname
		}
	}
	return req
}

// sendAlertNotification sends a normal push notification for an alert. It is
// best-effort: a failure is logged and recorded on the /history surface but
// never darkens the timeline path.
func (b *Bridge) sendAlertNotification(ctx context.Context, logger *slog.Logger, a alert, slug, alertname string, resolved bool) {
	req := buildAlertNotification(a, alertname, resolved)
	if err := b.pwClient.SendNotification(ctx, req); err != nil {
		logger.Warn("failed to send alert notification", "resolved", resolved, "error", err)
		b.recordError()
		b.record(alertname, slug, "notify", false, err.Error())
		return
	}
	b.recordPushSent()
	b.record(alertname, slug, "notified", true, req.Level)
}

// ruleUIDFor extracts the Grafana alert-rule UID from an alert's generatorURL,
// returning "" when no resolver is configured or the URL doesn't match. Stored
// on the alertState so the management UI can build a precise silence matcher
// (__alert_rule_uid__) for the in-UI "Silence" action.
func (b *Bridge) ruleUIDFor(a alert) string {
	if b.grafanaClient == nil {
		return ""
	}
	return b.grafanaClient.ExtractRuleUID(a.GeneratorURL)
}

// ActiveAlert is a read-only snapshot of one tracked alert, exposed to the
// management UI so a row can build silence matchers (ruleUID preferred,
// alertname as fallback).
type ActiveAlert struct {
	Slug      string `json:"slug"`
	AlertName string `json:"alertname"`
	RuleUID   string `json:"ruleUid"`
}

// ActiveAlerts returns a snapshot of the currently tracked alerts. Used by the
// /active resource route so the Activities page can map an activity slug back to
// the alert identity needed to silence it.
func (b *Bridge) ActiveAlerts() []ActiveAlert {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ActiveAlert, 0, len(b.active))
	for _, st := range b.active {
		out = append(out, ActiveAlert{Slug: st.slug, AlertName: st.alertname, RuleUID: st.ruleUID})
	}
	return out
}

// Forget drops the alert tracked under slug (if any) and stops its poller, so a
// user-initiated "End" from the management UI isn't resurrected by the next
// poll or alertmanager backstop. It waits for the poller goroutine to exit
// before returning (StopAndWait), so the caller's subsequent terminal ENDED
// patch can't be overtaken by an in-flight steady-state ongoing patch. Returns
// whether an entry was found.
func (b *Bridge) Forget(slug string) bool {
	b.mu.Lock()
	var key string
	for k, st := range b.active {
		if st.slug == slug {
			key = k
			break
		}
	}
	if key != "" {
		delete(b.active, key)
	}
	b.mu.Unlock()
	if b.poller != nil {
		// Always StopAndWait (even when the entry was already gone): a poller may
		// still be running for this slug, and draining it before the caller ends
		// the activity is what closes the resurrection window.
		b.poller.StopAndWait(slug)
	}
	return key != ""
}

func (b *Bridge) fetchHistoryAll(ctx context.Context, logger *slog.Logger, expr, seriesLabel string) map[string][]pushward.HistoryPoint {
	now := time.Now()
	step := b.cfg.HistoryWindow / 120
	if step < 15*time.Second {
		step = 15 * time.Second
	}

	allSeries, err := b.metricsClient.QueryRangeAll(ctx, expr, now.Add(-b.cfg.HistoryWindow), now, step)
	if err != nil {
		logger.Warn("failed to fetch history", "error", err)
		return nil
	}

	if len(allSeries) == 0 {
		return nil
	}

	history := make(map[string][]pushward.HistoryPoint, len(allSeries))
	for _, s := range allSeries {
		key := SeriesKey(s.Labels, seriesLabel)
		history[key] = s.Points
	}
	return history
}

func makeSlug(alertname string) string {
	return text.SlugHash("grafana", alertname, 8)
}

// startSweeper runs a background goroutine that removes stale entries from the
// active alerts map when Grafana fails to send a resolved webhook.
func (b *Bridge) startSweeper(ctx context.Context, maxAge time.Duration) {
	if maxAge <= 0 {
		return
	}
	b.bgWg.Add(1)
	go func() {
		defer b.bgWg.Done()
		ticker := time.NewTicker(maxAge / 2)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.sweepStale(maxAge)
			}
		}
	}()
}

func (b *Bridge) sweepStale(maxAge time.Duration) {
	now := time.Now()
	b.mu.Lock()
	for name, state := range b.active {
		if now.Sub(state.lastSeen) > maxAge {
			slog.Info("sweeping stale alert", "alertname", name, "slug", state.slug)
			b.poller.Stop(state.slug)
			delete(b.active, name)
		}
	}
	b.mu.Unlock()
}

// startAlertChecker runs a background goroutine that periodically queries the
// Grafana alertmanager API to detect resolved alerts when webhooks are missed.
// Requires the Grafana resolver to be configured.
func (b *Bridge) startAlertChecker(ctx context.Context, interval time.Duration) {
	if b.grafanaClient == nil || interval <= 0 {
		return
	}
	slog.Info("alert state checker enabled", "interval", interval)
	b.bgWg.Add(1)
	go func() {
		defer b.bgWg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.checkAlertStates(ctx)
			}
		}
	}()
}

func (b *Bridge) checkAlertStates(ctx context.Context) {
	b.mu.Lock()
	type entry struct {
		name  string
		state alertState // copy
	}
	entries := make([]entry, 0, len(b.active))
	for name, state := range b.active {
		entries = append(entries, entry{name: name, state: *state})
	}
	b.mu.Unlock()

	for _, e := range entries {
		// Anonymous alerts are keyed by fingerprint and have no alertname to
		// query the alertmanager filter with; let the sweeper's staleTimeout
		// reap them instead of mistaking the always-false query for "resolved".
		if e.state.alertname == "" {
			continue
		}
		firing, err := b.grafanaClient.IsAlertFiring(ctx, e.state.alertname)
		if err != nil {
			slog.Warn("alert state check failed", "alertname", e.state.alertname, "error", err)
			continue
		}
		if firing {
			continue
		}

		// Alert is no longer firing — end the activity.
		b.mu.Lock()
		cur, stillActive := b.active[e.name]
		// Only end if the entry hasn't been refreshed since our snapshot: a
		// firing webhook that re-fired the alert during the out-of-lock check
		// bumps lastSeen, and we must not tear down that fresh activity/poller.
		if stillActive && cur.lastSeen.Equal(e.state.lastSeen) {
			delete(b.active, e.name)
		} else {
			stillActive = false
		}
		b.mu.Unlock()

		if !stillActive {
			continue // resolved by webhook, or re-fired during the check
		}

		b.poller.Stop(e.state.slug)
		b.endAlertActivity(ctx, e.state.alertname, &e.state)
	}
}

// endAlertActivity sends an ENDED update for an alert that is no longer firing.
func (b *Bridge) endAlertActivity(ctx context.Context, alertname string, state *alertState) {
	logger := slog.With("alertname", alertname, "slug", state.slug)

	values := b.finalValues(ctx, state.expr, state.seriesLabel, state.lastValues)
	values, _ = capSeries(values, state.primary)

	content := pushward.Content{
		Template:    templateTimeline,
		Subtitle:    "Grafana",
		Icon:        resolvedIcon,
		AccentColor: pushward.ColorGreen,
		State:       "Resolved",
		Scale:       b.cfg.Scale,
		Smoothing:   b.cfg.Smoothing,
		Decimals:    b.cfg.Decimals,
	}
	if len(values) > 0 {
		content.Value = any(values)
	}

	if err := b.pwClient.UpdateActivity(ctx, state.slug, pushward.UpdateRequest{
		State:   pushward.StateEnded,
		Content: content,
	}); err != nil {
		logger.Error("failed to end resolved activity", "error", err)
		b.recordError()
		b.record(alertname, state.slug, "resolved", false, err.Error())
	} else {
		logger.Info("activity ended (alert no longer firing)")
		b.recordPushSent()
		b.record(alertname, state.slug, "resolved", true, "alert no longer firing")
	}

	// Also notify on the backstop resolve so a dropped resolved-webhook still
	// alerts the user. There is no webhook payload here, so the push carries only
	// the alert name (no instance/summary) - an empty alert yields the plain
	// "Grafana"/"Resolved" notification.
	if b.cfg.AlsoNotify {
		b.sendAlertNotification(ctx, logger, alert{}, state.slug, alertname, true)
	}
}

// record forwards a delivery outcome to the optional DeliveryLogger.
func (b *Bridge) record(alertname, slug, action string, ok bool, detail string) {
	if b.deliveryLog != nil {
		b.deliveryLog.Log(alertname, slug, action, ok, detail)
	}
}

func (b *Bridge) recordActivityCreated() {
	if b.metrics != nil {
		b.metrics.IncActivitiesCreated()
	}
}

func (b *Bridge) recordPushSent() {
	if b.metrics != nil {
		b.metrics.IncPushesSent()
	}
}

func (b *Bridge) recordError() {
	if b.metrics != nil {
		b.metrics.IncErrors()
	}
}

func parseAnnotationThreshold(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "> <!=")
	s = strings.TrimSpace(s)
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}
