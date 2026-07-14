package bridge

import (
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

// TestStopAndWaitDrains is the guard for the end-activity resurrection fix:
// StopAndWait must cancel the per-slug poller AND block until its goroutine has
// exited, so a caller's terminal ENDED patch can't be overtaken by an in-flight
// ongoing patch. A long interval keeps the goroutine parked on the ticker; the
// cancel must still unblock and drain it.
func TestStopAndWaitDrains(t *testing.T) {
	poller := NewPoller(nopQuerier{}, pushward.NewClient("http://127.0.0.1:0", "hlk_x"), time.Hour)
	t.Cleanup(func() {
		poller.StopAll()
		poller.Wait()
	})

	poller.StartWithSeed("slug", "up", "", "", nil)
	if n := poller.ActiveCount(); n != 1 {
		t.Fatalf("ActiveCount = %d, want 1 after start", n)
	}

	done := make(chan struct{})
	go func() {
		poller.StopAndWait("slug")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StopAndWait did not return (goroutine not drained)")
	}

	if n := poller.ActiveCount(); n != 0 {
		t.Fatalf("ActiveCount = %d, want 0 after StopAndWait", n)
	}
	// No-op on an unknown slug.
	poller.StopAndWait("does-not-exist")
}
