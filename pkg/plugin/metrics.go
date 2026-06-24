package plugin

import (
	"bytes"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

// bridgeMetrics holds the plugin's Prometheus counters and the dedicated
// registry they live on. It implements bridge.Metrics (the bridge increments
// the counters) and exposes Gather for the CollectMetrics handler. A private
// registry keeps the plugin's metrics isolated from any global default registry.
type bridgeMetrics struct {
	reg               *prometheus.Registry
	alertsReceived    prometheus.Counter
	activitiesCreated prometheus.Counter
	pushesSent        prometheus.Counter
	errorsTotal       prometheus.Counter
}

func newBridgeMetrics() *bridgeMetrics {
	m := &bridgeMetrics{
		reg: prometheus.NewRegistry(),
		alertsReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pushward_alerts_received_total",
			Help: "Total Grafana alert instances received by the bridge webhook.",
		}),
		activitiesCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pushward_activities_created_total",
			Help: "Total PushWard Live Activities created by the bridge.",
		}),
		pushesSent: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pushward_pushes_sent_total",
			Help: "Total activity create/update/end pushes the bridge sent to api.pushward.app.",
		}),
		errorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pushward_errors_total",
			Help: "Total errors encountered while delivering to api.pushward.app.",
		}),
	}
	m.reg.MustRegister(m.alertsReceived, m.activitiesCreated, m.pushesSent, m.errorsTotal)
	return m
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

// gather renders the registry's metrics in the Prometheus text exposition
// format for backend.CollectMetricsResult.PrometheusMetrics.
func (m *bridgeMetrics) gather() ([]byte, error) {
	mfs, err := m.reg.Gather()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
