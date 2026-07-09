package plugin

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestCountersOnDefaultRegistry guards the bug that made the shipped delivery
// dashboard permanently empty: the counters lived on a private registry, and
// backend.Manage only ever gathers prometheus.DefaultGatherer. Anything not on
// the default registry never reaches /metrics/plugins/pushward-alerts-app.
func TestCountersOnDefaultRegistry(t *testing.T) {
	sharedBridgeMetrics() // ensure registration has happened

	want := map[string]bool{
		"pushward_alerts_received_total":    false,
		"pushward_activities_created_total": false,
		"pushward_pushes_sent_total":        false,
		"pushward_errors_total":             false,
	}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather default registry: %s", err)
	}
	for _, mf := range families {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s missing from the default registry, so Grafana will never export it", name)
		}
	}
}

// TestSharedBridgeMetricsIsSingleton guards the panic that registering on the
// default registry would otherwise cause: NewApp is an instance factory the SDK
// re-invokes on every settings change, and a second MustRegister of the same
// counter fails with prometheus.AlreadyRegisteredError.
func TestSharedBridgeMetricsIsSingleton(t *testing.T) {
	first := sharedBridgeMetrics()
	second := sharedBridgeMetrics()
	if first != second {
		t.Fatal("sharedBridgeMetrics returned distinct instances; repeated NewApp calls would re-register and panic")
	}
}

// TestSnapshotReadsCounters checks the JSON surface reads the same collectors
// Prometheus scrapes, so /stats and /metrics can never disagree.
func TestSnapshotReadsCounters(t *testing.T) {
	m := sharedBridgeMetrics()
	before := m.Snapshot()

	m.IncAlertsReceived(3)
	m.IncActivitiesCreated()
	m.IncPushesSent()
	m.IncErrors()

	after := m.Snapshot()
	if got := after.AlertsReceived - before.AlertsReceived; got != 3 {
		t.Errorf("AlertsReceived delta = %v, want 3", got)
	}
	if got := after.ActivitiesCreated - before.ActivitiesCreated; got != 1 {
		t.Errorf("ActivitiesCreated delta = %v, want 1", got)
	}
	if got := after.PushesSent - before.PushesSent; got != 1 {
		t.Errorf("PushesSent delta = %v, want 1", got)
	}
	if got := after.Errors - before.Errors; got != 1 {
		t.Errorf("Errors delta = %v, want 1", got)
	}
}

// TestIncAlertsReceivedIgnoresNonPositive documents that a webhook carrying no
// alerts must not move the counter (prometheus panics on a negative Add).
func TestIncAlertsReceivedIgnoresNonPositive(t *testing.T) {
	m := sharedBridgeMetrics()
	before := m.Snapshot().AlertsReceived
	m.IncAlertsReceived(0)
	m.IncAlertsReceived(-2)
	if got := m.Snapshot().AlertsReceived; got != before {
		t.Errorf("AlertsReceived = %v, want unchanged %v", got, before)
	}
}
