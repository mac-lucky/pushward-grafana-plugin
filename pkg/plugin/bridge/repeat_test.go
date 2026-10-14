package bridge

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	"github.com/mac-lucky/pushward-integrations/shared/syncx"
)

// ruleResolver resolves every alert to one rule query.
type ruleResolver struct {
	expr, refID, datasourceUID string
}

func (ruleResolver) ExtractRuleUID(string) string { return "rule-1" }

func (r ruleResolver) GetRuleQuery(context.Context, string) (string, string, string, error) {
	return r.expr, r.refID, r.datasourceUID, nil
}

func (ruleResolver) IsAlertFiring(context.Context, string) (bool, error) { return true, nil }

// recQuerier answers every query with one series labeled instance=n1 and
// records the expressions it was asked to run.
type recQuerier struct {
	mu      sync.Mutex
	exprs   []string
	instant float64
}

func (q *recQuerier) QueryRangeAll(_ context.Context, expr string, _, _ time.Time, _ time.Duration) ([]LabeledSeries, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.exprs = append(q.exprs, expr)
	now := time.Now().Unix()
	return []LabeledSeries{{
		Labels: map[string]string{"instance": "n1"},
		Points: []pushward.HistoryPoint{{Timestamp: now - 60, Value: 1}, {Timestamp: now, Value: 2}},
	}}, nil
}

func (q *recQuerier) QueryInstantAll(_ context.Context, expr string, _ time.Time) ([]LabeledPoint, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.exprs = append(q.exprs, expr)
	return []LabeledPoint{{
		Labels: map[string]string{"instance": "n1"},
		Point:  pushward.HistoryPoint{Timestamp: time.Now().Unix(), Value: q.instant},
	}}, nil
}

func (q *recQuerier) ran(expr string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.exprs {
		if e == expr {
			return true
		}
	}
	return false
}

// newPollingBridge is newTestBridge with a poller that ticks every interval
// and reports back to the bridge, as NewBridge wires it.
func newPollingBridge(t *testing.T, url string, q MetricsQuerier, resolver GrafanaResolver, cfg Config, interval time.Duration) *Bridge {
	t.Helper()
	pw := pushward.NewClient(url, "hlk_x")
	poller := NewPoller(q, pw, interval)
	t.Cleanup(func() {
		poller.StopAll()
		poller.Wait()
	})
	b := &Bridge{
		pwClient:      pw,
		metricsClient: q,
		poller:        poller,
		grafanaClient: resolver,
		active:        make(map[string]*alertState),
		capDrops:      syncx.NewDropCounter(100),
		cfg:           cfg,
	}
	poller.SetUpdateCallback(b.recordPollerValues)
	return b
}

// activityWrite is one PATCH to an activity: a full timeline update when it
// carries the template, a value-only poller patch when it does not.
type activityWrite struct {
	full  bool
	state string
	value map[string]any
}

func (s *stubServer) activityWrites(slug string) []activityWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []activityWrite
	for _, r := range s.reqs {
		if r.method != http.MethodPatch || r.path != "/activities/"+slug {
			continue
		}
		content, _ := r.body["content"].(map[string]any)
		w := activityWrite{state: stringOf(r.body["state"])}
		_, w.full = content["template"]
		w.value, _ = content["value"].(map[string]any)
		out = append(out, w)
	}
	return out
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// waitFor polls cond until it holds or a second passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// repeatsOf counts the value-only patches that sent want.
func repeatsOf(ws []activityWrite, want map[string]any) int {
	n := 0
	for _, w := range ws {
		if !w.full && reflect.DeepEqual(w.value, want) {
			n++
		}
	}
	return n
}

func TestRepeatResendsWebhookValuesWhileFiring(t *testing.T) {
	s := newStubServer(t)
	b := newPollingBridge(t, s.URL, nopQuerier{}, nil, Config{}, 10*time.Millisecond)
	slug := makeSlug("HighCPU")

	a := firingAlert()
	a.Values = map[string]float64{"A": 56}
	b.handleFiring(context.Background(), a, nil)
	waitFor(t, "two repeats of the firing value", func() bool {
		return repeatsOf(s.activityWrites(slug), map[string]any{"HighCPU": 56.0}) >= 2
	})

	// A repeat webhook moves the repeated value along.
	a.Values = map[string]float64{"A": 58}
	b.handleFiring(context.Background(), a, nil)
	waitFor(t, "a repeat of the new value", func() bool {
		return repeatsOf(s.activityWrites(slug), map[string]any{"HighCPU": 58.0}) >= 1
	})

	r := resolvedAlert()
	r.Values = map[string]float64{"A": 54}
	b.handleResolved(context.Background(), r)
	if n := b.poller.ActiveCount(); n != 0 {
		t.Fatalf("poller still running %d goroutines after resolve", n)
	}

	// Let any tick already in flight land, then nothing may follow the end.
	time.Sleep(30 * time.Millisecond)
	ws := s.activityWrites(slug)
	end := -1
	for i, w := range ws {
		if w.state == pushward.StateEnded {
			end = i
		}
	}
	if end < 0 {
		t.Fatalf("no ENDED update among %+v", ws)
	}
	if got := ws[end].value; !reflect.DeepEqual(got, map[string]any{"HighCPU": 54.0}) {
		t.Errorf("ENDED value = %v, want the resolve's own value", got)
	}
	time.Sleep(30 * time.Millisecond)
	if after := s.activityWrites(slug)[end+1:]; len(after) != 0 {
		t.Errorf("writes after the ENDED update: %+v", after)
	}
}

func TestForgetStopsRepeat(t *testing.T) {
	s := newStubServer(t)
	b := newPollingBridge(t, s.URL, nopQuerier{}, nil, Config{}, 10*time.Millisecond)
	slug := makeSlug("HighCPU")

	a := firingAlert()
	a.Values = map[string]float64{"A": 1}
	b.handleFiring(context.Background(), a, nil)
	waitFor(t, "a repeat", func() bool {
		return repeatsOf(s.activityWrites(slug), map[string]any{"HighCPU": 1.0}) >= 1
	})

	b.Forget(context.Background(), slug)
	// A patch already on the wire when Forget canceled it can still reach the
	// stub; count from after it settles.
	time.Sleep(20 * time.Millisecond)
	n := len(s.activityWrites(slug))
	time.Sleep(40 * time.Millisecond)
	if got := len(s.activityWrites(slug)); got != n {
		t.Errorf("%d writes after Forget, want none", got-n)
	}
}

// A rule whose query targets another datasource (LogsQL, say) is never run
// against the polled one; its own ref ID picks its value out of the webhook's
// and that value repeats.
func TestRuleOnOtherDatasourceRepeatsItsOwnValue(t *testing.T) {
	s := newStubServer(t)
	q := &recQuerier{}
	res := ruleResolver{expr: `_time:5m error | stats count()`, refID: "A", datasourceUID: "logs"}
	b := newPollingBridge(t, s.URL, q, res, Config{DatasourceUID: "metrics"}, 10*time.Millisecond)
	slug := makeSlug("HighCPU")

	a := firingAlert()
	a.GeneratorURL = "http://grafana/alerting/grafana/rule-1/view"
	a.Values = map[string]float64{"A": 7, "B": 7, "C": 1}
	b.handleFiring(context.Background(), a, nil)

	want := map[string]any{"HighCPU": 7.0}
	ws := s.activityWrites(slug)
	if len(ws) == 0 || !ws[0].full || !reflect.DeepEqual(ws[0].value, want) {
		t.Fatalf("firing update = %+v, want a timeline update with %v", ws, want)
	}
	waitFor(t, "a repeat", func() bool { return repeatsOf(s.activityWrites(slug), want) >= 1 })
	if q.ran(res.expr) {
		t.Error("the other datasource's query was run against the polled one")
	}
}

func TestRuleOnPolledDatasourceRunsQueryPoller(t *testing.T) {
	s := newStubServer(t)
	q := &recQuerier{instant: 5}
	res := ruleResolver{expr: "up", refID: "A", datasourceUID: "metrics"}
	b := newPollingBridge(t, s.URL, q, res, Config{DatasourceUID: "metrics"}, 10*time.Millisecond)
	slug := makeSlug("HighCPU")
	key := SeriesKey(map[string]string{"instance": "n1"}, "")

	a := firingAlert()
	a.GeneratorURL = "http://grafana/alerting/grafana/rule-1/view"
	a.Values = map[string]float64{"A": 9}
	b.handleFiring(context.Background(), a, nil)

	ws := s.activityWrites(slug)
	if len(ws) == 0 || !ws[0].full || !reflect.DeepEqual(ws[0].value, map[string]any{key: 2.0}) {
		t.Fatalf("firing update = %+v, want the history's latest value under %q", ws, key)
	}
	waitFor(t, "a polled value", func() bool {
		return repeatsOf(s.activityWrites(slug), map[string]any{key: 5.0}) >= 1
	})

	// A repeat webhook keeps the poller's series keys: the webhook's own
	// would prune the polled history server-side.
	q.mu.Lock()
	q.instant = 6
	q.mu.Unlock()
	b.handleFiring(context.Background(), a, nil)
	var last activityWrite
	for _, w := range s.activityWrites(slug) {
		if w.full {
			last = w
		}
	}
	if !reflect.DeepEqual(last.value, map[string]any{key: 6.0}) {
		t.Errorf("repeat webhook update value = %v, want the polled series {%s: 6}", last.value, key)
	}
}

func TestNotifyOnlyAlertStartsNoRepeat(t *testing.T) {
	s := newStubServer(t)
	b := newPollingBridge(t, s.URL, nopQuerier{}, nil, Config{ActivityMode: ActivityModeLabeled}, 10*time.Millisecond)

	a := firingAlert()
	a.Values = map[string]float64{"A": 1}
	b.handleFiring(context.Background(), a, nil)
	if n := b.poller.ActiveCount(); n != 0 {
		t.Errorf("poller running %d goroutines for an alert without a Live Activity", n)
	}
}

// Value-only patches carry no state, so the server keeps the stored one: an
// ONGOING there would restart an activity the user swiped away.
func TestPollerPatchesCarryNoState(t *testing.T) {
	s := newStubServer(t)
	b := newPollingBridge(t, s.URL, nopQuerier{}, nil, Config{}, 10*time.Millisecond)
	slug := makeSlug("HighCPU")

	a := firingAlert()
	a.Values = map[string]float64{"A": 3}
	b.handleFiring(context.Background(), a, nil)
	waitFor(t, "a repeat", func() bool {
		return repeatsOf(s.activityWrites(slug), map[string]any{"HighCPU": 3.0}) >= 1
	})
	for _, w := range s.activityWrites(slug) {
		if !w.full && w.state != "" {
			t.Errorf("value-only patch sent state %q", w.state)
		}
	}
}

func TestPollerStopsWhileRefusedAndRestartsOnNextWebhook(t *testing.T) {
	var refuse atomic.Bool
	slug := makeSlug("HighCPU")
	s := newStubServerFunc(t, func(path string) int {
		if path == "/activities/"+slug && refuse.Load() {
			return http.StatusUnprocessableEntity // e.g. the activity was preempted
		}
		return http.StatusOK
	})
	b := newPollingBridge(t, s.URL, nopQuerier{}, nil, Config{}, 10*time.Millisecond)

	a := firingAlert()
	a.Values = map[string]float64{"A": 3}
	b.handleFiring(context.Background(), a, nil)
	refuse.Store(true)
	waitFor(t, "the refused poller to stop", func() bool { return b.poller.ActiveCount() == 0 })

	refuse.Store(false)
	b.handleFiring(context.Background(), a, nil)
	if n := b.poller.ActiveCount(); n != 1 {
		t.Errorf("poller count after the next webhook = %d, want 1", n)
	}
}

func TestPollRefused(t *testing.T) {
	quota := &pushward.QuotaExceededError{HTTPError: &pushward.HTTPError{StatusCode: http.StatusTooManyRequests, Code: pushward.ErrCodeQuotaExceeded}}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"quota spent", quota, true},
		{"activity gone", &pushward.HTTPError{StatusCode: http.StatusNotFound}, true},
		{"preempted", &pushward.HTTPError{StatusCode: http.StatusUnprocessableEntity}, true},
		{"rate limited", &pushward.HTTPError{StatusCode: http.StatusTooManyRequests}, false},
		{"conflict", &pushward.HTTPError{StatusCode: http.StatusConflict}, false},
		{"server error", &pushward.HTTPError{StatusCode: http.StatusBadGateway}, false},
		{"network", errors.New("connection reset"), false},
	} {
		if got := pollRefused(tc.err); got != tc.want {
			t.Errorf("%s: pollRefused = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCappedAccountStartsNoPoller(t *testing.T) {
	s := newStubServer(t)
	never := func(context.Context) bool { return false }
	b := newPollingBridge(t, s.URL, nopQuerier{}, nil, Config{CanPoll: never}, 10*time.Millisecond)

	a := firingAlert()
	a.Values = map[string]float64{"A": 3}
	b.handleFiring(context.Background(), a, nil)
	if n := b.poller.ActiveCount(); n != 0 {
		t.Errorf("poller running %d goroutines on a capped account", n)
	}
	if ws := s.activityWrites(makeSlug("HighCPU")); len(ws) != 1 || !ws[0].full {
		t.Errorf("writes = %+v, want just the webhook's timeline update", ws)
	}
}

// gatedResolver holds its first rule lookup until release closes, the window
// in which a second instance's webhook used to see no query yet.
type gatedResolver struct {
	ruleResolver
	calls   atomic.Int32
	release chan struct{}
}

func (r *gatedResolver) GetRuleQuery(ctx context.Context, uid string) (string, string, string, error) {
	if r.calls.Add(1) == 1 {
		<-r.release
	}
	return r.ruleResolver.GetRuleQuery(ctx, uid)
}

func TestConcurrentInstancesShareTheQueryPoller(t *testing.T) {
	s := newStubServer(t)
	q := &recQuerier{instant: 5}
	res := &gatedResolver{ruleResolver: ruleResolver{expr: "up", refID: "A", datasourceUID: "metrics"}, release: make(chan struct{})}
	b := newPollingBridge(t, s.URL, q, res, Config{DatasourceUID: "metrics"}, 10*time.Millisecond)
	slug := makeSlug("HighCPU")

	first := firingAlert()
	first.GeneratorURL = "http://grafana/alerting/grafana/rule-1/view"
	first.Values = map[string]float64{"A": 9}
	second := first
	second.Fingerprint = "fp2"

	done := make(chan struct{})
	go func() {
		b.handleFiring(context.Background(), first, nil)
		close(done)
	}()
	waitFor(t, "the first instance's rule lookup", func() bool { return res.calls.Load() == 1 })
	b.handleFiring(context.Background(), second, nil)
	close(res.release)
	<-done
	time.Sleep(40 * time.Millisecond)

	for _, w := range s.activityWrites(slug) {
		if _, ok := w.value["HighCPU"]; ok {
			t.Fatalf("a write used the webhook's series key: %+v", w)
		}
	}
	if !q.ran("up") {
		t.Error("the query poller never ran")
	}
}

// failAfterFirst resolves the rule once and fails every later lookup.
type failAfterFirst struct {
	ruleResolver
	calls atomic.Int32
}

func (r *failAfterFirst) GetRuleQuery(ctx context.Context, uid string) (string, string, string, error) {
	if r.calls.Add(1) > 1 {
		return "", "", "", errors.New("503")
	}
	return r.ruleResolver.GetRuleQuery(ctx, uid)
}

// A repeat webhook reuses the query found at create, so a failing lookup then
// cannot switch the series keys from the rule's own value to A/B/C.
func TestRepeatWebhookReusesStoredQuery(t *testing.T) {
	s := newStubServer(t)
	res := &failAfterFirst{ruleResolver: ruleResolver{expr: "logs query", refID: "A", datasourceUID: "logs"}}
	b := newPollingBridge(t, s.URL, nopQuerier{}, res, Config{DatasourceUID: "metrics"}, time.Hour)
	slug := makeSlug("HighCPU")

	a := firingAlert()
	a.GeneratorURL = "http://grafana/alerting/grafana/rule-1/view"
	a.Values = map[string]float64{"A": 7, "B": 7, "C": 1}
	b.handleFiring(context.Background(), a, nil)
	a.Values = map[string]float64{"A": 8, "B": 8, "C": 1}
	b.handleFiring(context.Background(), a, nil)

	ws := s.activityWrites(slug)
	if len(ws) != 2 || !reflect.DeepEqual(ws[1].value, map[string]any{"HighCPU": 8.0}) {
		t.Errorf("writes = %+v, want the repeat webhook keyed HighCPU", ws)
	}
	if n := res.calls.Load(); n != 1 {
		t.Errorf("%d rule lookups, want 1", n)
	}
}

// With several instances under one alert name, the repeat follows the latest
// webhook among those still firing, not a resolved one's value.
func TestRepeatFollowsInstancesStillFiring(t *testing.T) {
	s := newStubServer(t)
	b := newPollingBridge(t, s.URL, nopQuerier{}, nil, Config{}, 10*time.Millisecond)
	slug := makeSlug("HighCPU")
	hot, cool := map[string]any{"HighCPU": 95.0}, map[string]any{"HighCPU": 60.0}

	host1 := firingAlert()
	host1.Values = map[string]float64{"A": 95}
	host2 := firingAlert()
	host2.Fingerprint = "fp2"
	host2.Values = map[string]float64{"A": 60}
	b.handleFiring(context.Background(), host1, nil)
	b.handleFiring(context.Background(), host2, nil)
	waitFor(t, "host2's value repeating", func() bool { return repeatsOf(s.activityWrites(slug), cool) >= 1 })

	r := resolvedAlert()
	r.Fingerprint = "fp2"
	b.handleResolved(context.Background(), r)
	before := repeatsOf(s.activityWrites(slug), hot)
	waitFor(t, "host1's value repeating again", func() bool { return repeatsOf(s.activityWrites(slug), hot) > before })
}
