package bridge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
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

// FetchFunc returns the values one poll tick sends. Empty values skip the tick.
type FetchFunc func(ctx context.Context) (map[string]float64, error)

// Poller manages per-alert polling goroutines that query Prometheus/VM
// (through the Grafana datasource proxy), or re-send an alert's last known
// values (StartRepeat), and push timeline updates to PushWard.
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
// the generic template while ONGOING. Reports whether it started a poller.
func (p *Poller) StartWithSeed(slug, expr, seriesLabel, primary string, seed *pushward.Content) bool {
	return p.start(slug, p.queryFetch(expr, seriesLabel), primary, seed)
}

// StartRepeat polls slug with fetch in place of a PromQL query: each tick sends
// whatever fetch returns. The bridge uses it for alerts it has no query to poll
// for, re-sending their last known values so the timeline keeps filling while
// they fire. The activity must already be on the timeline template; a repeat
// never seeds. No-op if already polling for this slug; reports whether it
// started a poller.
func (p *Poller) StartRepeat(slug, primary string, fetch FetchFunc) bool {
	return p.start(slug, fetch, primary, nil)
}

func (p *Poller) start(slug string, fetch FetchFunc, primary string, seed *pushward.Content) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.active[slug]; ok {
		return false
	}

	ctx, cancel := context.WithCancel(context.Background()) // #nosec G118 -- cancel is stored in p.active and called in Stop/StopAll
	h := &pollHandle{cancel: cancel, done: make(chan struct{})}
	p.active[slug] = h
	p.wg.Add(1)
	go p.run(ctx, slug, h, fetch, primary, seed)
	return true
}

// queryFetch returns a FetchFunc that runs expr as an instant query and keys
// each series by SeriesKey.
func (p *Poller) queryFetch(expr, seriesLabel string) FetchFunc {
	return func(ctx context.Context) (map[string]float64, error) {
		points, err := p.metricsClient.QueryInstantAll(ctx, expr, time.Now())
		if err != nil {
			return nil, err
		}
		values := make(map[string]float64, len(points))
		for _, lp := range points {
			values[SeriesKey(lp.Labels, seriesLabel)] = lp.Point.Value
		}
		return values, nil
	}
}

// Stop cancels the polling goroutine for the given slug (asynchronous; does not
// wait for it to exit). Used by the stale sweep, which holds b.mu and sends
// no terminal update; the paths that end an activity use StopAndWait.
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

func (p *Poller) run(ctx context.Context, slug string, h *pollHandle, fetch FetchFunc, primary string, seed *pushward.Content) {
	defer p.wg.Done()
	defer close(h.done) // signal StopAndWait that this goroutine has fully exited

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
			var refused bool
			seeded, refused = p.poll(ctx, logger, slug, fetch, primary, seed, seeded)
			if refused {
				// Free the slot so the alert's next webhook can start a
				// poller again; leave it alone if a Stop already took it.
				p.mu.Lock()
				if p.active[slug] == h {
					delete(p.active, slug)
				}
				p.mu.Unlock()
				h.cancel()
				logger.Info("poller stopped until the alert's next webhook: PushWard refuses its updates")
				return
			}
		}
	}
}

// pollRefused reports whether PushWard will keep refusing an activity's
// updates, so polling on would only spend requests: its Live Activity quota is
// spent until the month ends, or the activity is gone, preempted (swiped away
// or evicted) or otherwise not patchable, which comes back as a 4xx. A rate
// limit, a timeout, a conflict and a 5xx are worth the next tick.
func pollRefused(err error) bool {
	var qe *pushward.QuotaExceededError
	if errors.As(err, &qe) {
		return true
	}
	var he *pushward.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	switch he.StatusCode {
	case http.StatusTooManyRequests, http.StatusRequestTimeout, http.StatusConflict:
		return false
	}
	return he.StatusCode >= 400 && he.StatusCode < 500
}

// poll fetches the values and pushes an update. It returns the (possibly
// updated) seeded state and whether PushWard refused the update for good (see
// pollRefused).
func (p *Poller) poll(ctx context.Context, logger *slog.Logger, slug string, fetch FetchFunc, primary string, seed *pushward.Content, seeded bool) (bool, bool) {
	values, err := fetch(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return seeded, false
		}
		logger.Warn("poll failed", "error", err)
		return seeded, false
	}

	if len(values) == 0 {
		return seeded, false
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
				return seeded, false
			}
			logger.Warn("poll seed update failed", "error", err)
			return seeded, pollRefused(err)
		}
		logger.Info("activity seeded by poller on first values")
		seeded = true
	} else {
		// Merge-patch with just the new sample. Template/units/accent/display
		// config were seeded already and are preserved server-side. No state:
		// the server keeps the stored one. Sending ONGOING would restart an
		// activity the user swiped away or PushWard preempted, with its
		// history dropped; a preempted one refuses a stateless patch instead.
		if err := p.pwClient.PatchActivity(ctx, slug, pushward.PatchRequest{
			Content: &pushward.ContentPatch{Value: values},
		}); err != nil {
			if ctx.Err() != nil {
				return seeded, false
			}
			logger.Warn("poll update failed", "error", err)
			return seeded, pollRefused(err)
		}
	}

	p.mu.Lock()
	cb := p.callback
	p.mu.Unlock()
	if cb != nil {
		cb(slug, values)
	}
	return seeded, false
}
