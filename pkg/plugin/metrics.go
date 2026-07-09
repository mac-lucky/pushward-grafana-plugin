package plugin

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	dto "github.com/prometheus/client_model/go"
)

// bridgeMetrics holds the plugin's Prometheus counters. It implements
// bridge.Metrics (the bridge increments the counters) and Snapshot (the /stats
// resource route reads them back).
//
// The counters live on prometheus.DefaultRegisterer because that is the only
// registry the plugin SDK exports: backend.Manage hands
// prometheus.DefaultGatherer to the gRPC diagnostics adapter, which is what
// Grafana serves at /metrics/plugins/<plugin-id>. A private registry would be
// gathered by nothing.
//
// They are also plain counters rather than counter vectors on purpose: an
// unlabelled counter is exported at 0 from process start, so a scraped but idle
// bridge reads 0 instead of the series disappearing entirely.
type bridgeMetrics struct {
	alertsReceived    prometheus.Counter
	activitiesCreated prometheus.Counter
	pushesSent        prometheus.Counter
	errorsTotal       prometheus.Counter
}

// BridgeStats is the counter snapshot served by GET /stats.
type BridgeStats struct {
	AlertsReceived    float64 `json:"alertsReceived"`
	ActivitiesCreated float64 `json:"activitiesCreated"`
	PushesSent        float64 `json:"pushesSent"`
	Errors            float64 `json:"errors"`
}

// sharedBridgeMetrics returns the process-wide counters. NewApp is an instance
// factory that the SDK re-invokes whenever the app settings change, so the
// counters must be built once: registering them twice would panic with
// prometheus.AlreadyRegisteredError, and rebuilding them would reset every
// counter on each config save.
var sharedBridgeMetrics = sync.OnceValue(newBridgeMetrics)

func newBridgeMetrics() *bridgeMetrics {
	auto := promauto.With(prometheus.DefaultRegisterer)
	return &bridgeMetrics{
		alertsReceived: auto.NewCounter(prometheus.CounterOpts{
			Name: "pushward_alerts_received_total",
			Help: "Total Grafana alert instances received by the bridge webhook.",
		}),
		activitiesCreated: auto.NewCounter(prometheus.CounterOpts{
			Name: "pushward_activities_created_total",
			Help: "Total PushWard Live Activities created by the bridge.",
		}),
		pushesSent: auto.NewCounter(prometheus.CounterOpts{
			Name: "pushward_pushes_sent_total",
			Help: "Total activity create/update/end pushes the bridge sent to api.pushward.app.",
		}),
		errorsTotal: auto.NewCounter(prometheus.CounterOpts{
			Name: "pushward_errors_total",
			Help: "Total errors encountered while delivering to api.pushward.app.",
		}),
	}
}

// IncAlertsReceived implements bridge.Metrics.
func (m *bridgeMetrics) IncAlertsReceived(n int) {
	if n > 0 {
		m.alertsReceived.Add(float64(n))
	}
}

// IncActivitiesCreated implements bridge.Metrics.
func (m *bridgeMetrics) IncActivitiesCreated() { m.activitiesCreated.Inc() }

// IncPushesSent implements bridge.Metrics.
func (m *bridgeMetrics) IncPushesSent() { m.pushesSent.Inc() }

// IncErrors implements bridge.Metrics.
func (m *bridgeMetrics) IncErrors() { m.errorsTotal.Inc() }

// Snapshot reads the current counter values straight off the collectors, so the
// JSON surface and the Prometheus surface can never drift apart.
func (m *bridgeMetrics) Snapshot() BridgeStats {
	return BridgeStats{
		AlertsReceived:    counterValue(m.alertsReceived),
		ActivitiesCreated: counterValue(m.activitiesCreated),
		PushesSent:        counterValue(m.pushesSent),
		Errors:            counterValue(m.errorsTotal),
	}
}

// counterValue extracts a counter's value. Write only fails for collectors that
// cannot describe themselves, which a plain counter always can, so a read error
// is reported as 0 rather than plumbed through every caller.
func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}
