package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// At most maxConcurrentWebhooks webhooks are processed at once; one more finds
// no slot and gets ErrBusy, and a slot frees once a webhook is done.
func TestWebhookSlots(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
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

	for i := range maxConcurrentWebhooks {
		if err := b.ProcessWebhook(context.Background(), payload(i)); err != nil {
			t.Fatalf("webhook %d: %v, want it taken", i, err)
		}
	}
	raw := payload(maxConcurrentWebhooks)
	// A cancelled context gives up at once instead of waiting webhookSlotWait.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.ProcessWebhook(ctx, raw); !errors.Is(err, ErrBusy) {
		t.Fatalf("webhook past the limit: %v, want ErrBusy", err)
	}

	close(release)
	b.wg.Wait()
	if err := b.ProcessWebhook(ctx, raw); err != nil {
		t.Errorf("webhook after the others finished: %v, want it taken", err)
	}
	b.wg.Wait()
}
