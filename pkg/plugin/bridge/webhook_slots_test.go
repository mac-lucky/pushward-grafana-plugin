package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// At most maxConcurrentWebhooks webhooks are processed at once and
// webhookQueueCap more wait their turn; one past both gets ErrBusy. Every
// webhook taken is processed, and the queue takes more once it drains.
func TestWebhookQueue(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		<-release
		inFlight.Add(-1)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(open) // runs first: no handler left blocked on Close
	b := newTestBridge(t, srv.URL, nil, Config{SeverityLabel: "severity", DefaultSeverity: "warning"})
	// A new alert name per webhook, so each one waits on its own create.
	payload := func(i int) []byte {
		a := firingAlert()
		a.Labels["alertname"] = fmt.Sprint("Alert", i)
		raw, err := json.Marshal(webhookPayload{Alerts: []alert{a}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	taken := maxConcurrentWebhooks + webhookQueueCap
	for i := range taken {
		if err := b.ProcessWebhook(context.Background(), payload(i)); err != nil {
			t.Fatalf("webhook %d: %v, want it taken", i, err)
		}
	}
	raw := payload(taken)
	if err := b.ProcessWebhook(context.Background(), raw); !errors.Is(err, ErrBusy) {
		t.Fatalf("webhook past the queue: %v, want ErrBusy", err)
	}
	for deadline := time.Now().Add(5 * time.Second); inFlight.Load() < maxConcurrentWebhooks && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}

	open()
	b.wg.Wait()
	if p := peak.Load(); p != maxConcurrentWebhooks {
		t.Errorf("%d webhooks processed at once, want %d", p, maxConcurrentWebhooks)
	}
	if n := b.ActiveCount(); n != taken {
		t.Errorf("%d alerts tracked, want all %d webhooks taken processed", n, taken)
	}
	if err := b.ProcessWebhook(context.Background(), raw); err != nil {
		t.Errorf("webhook after the queue drained: %v, want it taken", err)
	}
	b.wg.Wait()
}
