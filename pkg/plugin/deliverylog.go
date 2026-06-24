package plugin

import (
	"sync"
	"time"
)

// deliveryLogCap bounds the in-memory delivery-log ring buffer. The /history
// surface is a recent-activity view, not an archive, so an old entry is
// overwritten rather than grown unbounded.
const deliveryLogCap = 200

// detailMaxLen bounds a single entry's detail string (typically an error
// message) so a pathological error can't bloat the buffer.
const detailMaxLen = 256

// DeliveryEntry is one recorded bridge outcome surfaced by GET /history. JSON
// tags match the contract response shape.
type DeliveryEntry struct {
	Ts        int64  `json:"ts"`
	Alertname string `json:"alertname"`
	Slug      string `json:"slug"`
	Action    string `json:"action"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
}

// DeliveryLog is a thread-safe fixed-capacity ring buffer of recent bridge
// deliveries. The bridge appends via Log (satisfying bridge.DeliveryLogger);
// the /history resource reads via Entries.
type DeliveryLog struct {
	mu      sync.Mutex
	entries []DeliveryEntry
	next    int  // index of the next write
	full    bool // whether the buffer has wrapped
}

// NewDeliveryLog returns an empty delivery log.
func NewDeliveryLog() *DeliveryLog {
	return &DeliveryLog{entries: make([]DeliveryEntry, deliveryLogCap)}
}

// Log records a delivery outcome. It is safe for concurrent use.
func (l *DeliveryLog) Log(alertname, slug, action string, ok bool, detail string) {
	if len(detail) > detailMaxLen {
		detail = detail[:detailMaxLen]
	}
	l.mu.Lock()
	l.entries[l.next] = DeliveryEntry{
		Ts:        time.Now().Unix(),
		Alertname: alertname,
		Slug:      slug,
		Action:    action,
		OK:        ok,
		Detail:    detail,
	}
	l.next = (l.next + 1) % deliveryLogCap
	if l.next == 0 {
		l.full = true
	}
	l.mu.Unlock()
}

// Entries returns a snapshot of recorded entries, newest first.
func (l *DeliveryLog) Entries() []DeliveryEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := l.next
	if l.full {
		n = deliveryLogCap
	}
	out := make([]DeliveryEntry, 0, n)
	// Walk backwards from the most recent write so the result is newest-first.
	for i := 0; i < n; i++ {
		idx := (l.next - 1 - i + deliveryLogCap) % deliveryLogCap
		out = append(out, l.entries[idx])
	}
	return out
}
