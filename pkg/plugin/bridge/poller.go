package bridge

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

// MetricsQuerier is the subset of the metrics client the bridge and poller
// depend on. *MetricsClient satisfies it; abstracting it keeps the poller and
// handler testable with a stub and lets the proxy-backed querier be swapped in.
type MetricsQuerier interface {
	QueryRangeAll(ctx context.Context, expr string, from, to time.Time, step time.Duration) ([]LabeledSeries, error)
	QueryInstantAll(ctx context.Context, expr string, ts time.Time) ([]LabeledPoint, error)
}

// UpdateCallback is invoked after each successful poll with the values sent.
// Used by the handler to track the last series keys and values per slug so
// they can be reused on alert resolve (keeping keys stable prevents the
// server's AccumulateHistory from pruning prior series).
type UpdateCallback func(slug string, values map[string]float64)

// Poller manages per-alert polling goroutines that query Prometheus/VM
// (through the Grafana datasource proxy) and push timeline updates to PushWard.
type Poller struct {
	metricsClient MetricsQuerier
	pwClient      *pushward.Client
	interval      time.Duration

	mu       sync.Mutex
	active   map[string]*pollHandle
	callback UpdateCallback
	wg       sync.WaitGroup
}

// pollHandle tracks one per-slug poll goroutine: cancel stops it, done is closed
// when run() exits so StopAndWait can drain it deterministically.
type pollHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewPoller creates a new Poller. q is the metrics querier (the datasource-proxy
// client in the plugin); pw is the PushWard API client; interval is the poll
// cadence.
func NewPoller(q MetricsQuerier, pw *pushward.Client, interval time.Duration) *Poller {
	return &Poller{
		metricsClient: q,
		pwClient:      pw,
		interval:      interval,
		active:        make(map[string]*pollHandle),
	}
}

// Start begins polling for the given slug and PromQL expression.
// seriesLabel is the preferred metric label to use as series key (can be empty for auto-detect).
// primary is the series that drives the iOS headline; it is retained when a poll
// yields more than the server's series limit and capSeries has to trim (empty is fine).
// No-op if already polling for this slug. Use Start when the firing webhook has
// already seeded the activity's timeline template/styling.
func (p *Poller) Start(slug, expr, seriesLabel, primary string) {
	p.StartWithSeed(slug, expr, seriesLabel, primary, nil)
}

// StartWithSeed is like Start but, when seed is non-nil, the poller sends a full
// UpdateActivity establishing the timeline template/styling on its first
// successful tick (then value-only patches thereafter). Used when the firing
// webhook had no values to seed with, so the activity would otherwise be left on
// the generic template while ONGOING.
func (p *Poller) StartWithSeed(slug, expr, seriesLabel, primary string, seed *pushward.Content) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.active[slug]; ok {
		return
	}

	ctx, cancel := context.WithCancel(context.Background()) // #nosec G118 -- cancel is stored in p.active and called in Stop/StopAll
	done := make(chan struct{})
	p.active[slug] = &pollHandle{cancel: cancel, done: done}
	p.wg.Add(1)
	go p.run(ctx, slug, expr, seriesLabel, primary, seed, done)
}

// Stop cancels the polling goroutine for the given slug (asynchronous; does not
// wait for it to exit). Used by the hot resolve/sweep/checker paths.
func (p *Poller) Stop(slug string) {
	p.mu.Lock()
	h, ok := p.active[slug]
	if ok {
		delete(p.active, slug)
	}
	p.mu.Unlock()

	if ok {
		h.cancel()
	}
}

// StopAndWait cancels the polling goroutine for slug and blocks until it has
// exited, so a caller that then writes a terminal state can't be overtaken by an
// in-flight steady-state patch. A timeout bounds the wait so a goroutine stuck
// in a slow network call can't hang the caller. No-op if not polling that slug.
func (p *Poller) StopAndWait(slug string) {
	p.mu.Lock()
	h, ok := p.active[slug]
	if ok {
		delete(p.active, slug)
	}
	p.mu.Unlock()

	if !ok {
		return
	}
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
	}
}

// Wait blocks until all polling goroutines have exited.
func (p *Poller) Wait() {
	p.wg.Wait()
}

// StopAll cancels all active polling goroutines.
func (p *Poller) StopAll() {
	p.mu.Lock()
	for slug, h := range p.active {
		h.cancel()
		delete(p.active, slug)
	}
	p.mu.Unlock()
}

// ActiveCount returns the number of active polling goroutines.
func (p *Poller) ActiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.active)
}

// SetUpdateCallback registers a callback invoked after each successful poll.
// Safe to call concurrently with active polls; the callback is read under
// p.mu on each tick.
func (p *Poller) SetUpdateCallback(cb UpdateCallback) {
	p.mu.Lock()
	p.callback = cb
	p.mu.Unlock()
}

func (p *Poller) run(ctx context.Context, slug, expr, seriesLabel, primary string, seed *pushward.Content, done chan struct{}) {
	defer p.wg.Done()
	defer close(done) // signal StopAndWait that this goroutine has fully exited

	logger := slog.With("slug", slug)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	// seeded is false only when the firing webhook couldn't seed the activity
	// (no values); the first successful tick then sends the full timeline seed.
	seeded := seed == nil

	for {
		select {
		case <-ctx.Done():
			logger.Info("poller stopped")
			return
		case <-ticker.C:
			seeded = p.poll(ctx, logger, slug, expr, seriesLabel, primary, seed, seeded)
		}
	}
}

// poll queries the metric and pushes an update, returning the (possibly updated)
// seeded state.
func (p *Poller) poll(ctx context.Context, logger *slog.Logger, slug, expr, seriesLabel, primary string, seed *pushward.Content, seeded bool) bool {
	points, err := p.metricsClient.QueryInstantAll(ctx, expr, time.Now())
	if err != nil {
		if ctx.Err() != nil {
			return seeded
		}
		logger.Warn("poll failed", "error", err)
		return seeded
	}

	if len(points) == 0 {
		return seeded
	}

	values := make(map[string]float64, len(points))
	for _, lp := range points {
		key := SeriesKey(lp.Labels, seriesLabel)
		values[key] = lp.Point.Value
	}
	if capped, trimmed := capSeries(values, primary); trimmed {
		// Steady-state continuation of a condition handleFiring already Warn'd
		// once; Debug here so a persistently oversized series set doesn't spam
		// Warn every tick for the life of the alert.
		logger.Debug("timeline series capped to server limit",
			"limit", maxTimelineSeries, "dropped", len(values)-len(capped))
		values = capped
	}

	if !seeded {
		// The firing webhook had no values, so establish the timeline
		// template/styling now via a full UpdateActivity before switching to
		// value-only patches. Leave seeded=false on failure so we retry.
		content := *seed
		content.Value = values
		if err := p.pwClient.UpdateActivity(ctx, slug, pushward.UpdateRequest{
			State:   pushward.StateOngoing,
			Content: content,
		}); err != nil {
			if ctx.Err() != nil {
				return seeded
			}
			logger.Warn("poll seed update failed", "error", err)
			return seeded
		}
		logger.Info("activity seeded by poller on first values")
		seeded = true
	} else {
		// Merge-patch with just the new sample. Template/units/accent/display
		// config were seeded already and are preserved server-side.
		if err := p.pwClient.PatchActivity(ctx, slug, pushward.PatchRequest{
			State:   pushward.StateOngoing,
			Content: &pushward.ContentPatch{Value: values},
		}); err != nil {
			if ctx.Err() != nil {
				return seeded
			}
			logger.Warn("poll update failed", "error", err)
			return seeded
		}
	}

	p.mu.Lock()
	cb := p.callback
	p.mu.Unlock()
	if cb != nil {
		cb(slug, values)
	}
	return seeded
}
