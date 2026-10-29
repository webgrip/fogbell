// Package metrics is fogbell's own Prometheus metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Buckets are the time-to-acknowledge and time-to-resolve buckets in seconds: a minute to a week.
var Buckets = []float64{60, 300, 600, 1800, 3600, 7200, 14400, 43200, 86400, 259200, 604800}

// Metrics holds every collector.
type Metrics struct {
	Registry      *prometheus.Registry
	SlackRequests *prometheus.CounterVec
	Notifications *prometheus.CounterVec
	LastSuccess   prometheus.Gauge
	Interactions  *prometheus.CounterVec
	Interactive   prometheus.Gauge
	Incidents     *prometheus.CounterVec
	AckSeconds    *prometheus.HistogramVec
	ResolveSecs   *prometheus.HistogramVec
	Noise         *prometheus.CounterVec
	Nested        *prometheus.CounterVec
	Escalations   *prometheus.CounterVec
	Refreshes     *prometheus.CounterVec
	Enrichment    *prometheus.CounterVec
	Degraded      *prometheus.CounterVec
	HistoryFailed prometheus.Counter
}

// New registers every collector on a fresh registry.
func New() *Metrics {
	r := prometheus.NewRegistry()
	timing := []string{"alertname", "severity", "team"}
	m := &Metrics{
		Registry: r,
		SlackRequests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_slack_requests_total",
			Help: "Slack Web API calls by method and result (ok or Slack's error code)."}, []string{"method", "result"}),
		Notifications: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_notifications_total",
			Help: "Alertmanager notifications handled, by what was done."}, []string{"result"}),
		LastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{Name: "fogbell_last_success_timestamp_seconds",
			Help: "When a notification was last handled without an error."}),
		Interactions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_interactions_total",
			Help: "Slack interactions by action; rejected is a bad signature, error a failure."}, []string{"action"}),
		Interactive: prometheus.NewGauge(prometheus.GaugeOpts{Name: "fogbell_interactive",
			Help: "1 while a signing secret is present and the buttons that act are shown."}),
		Incidents: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_incidents_total",
			Help: "Incidents posted or reopened."}, timing),
		AckSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "fogbell_ack_seconds",
			Help: "Seconds from a firing being shown to the first I'm on it.", Buckets: Buckets}, timing),
		ResolveSecs: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "fogbell_resolve_seconds",
			Help: "Seconds from a firing being shown to its resolve.", Buckets: Buckets}, timing),
		Noise: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_noise_votes_total",
			Help: "Noise votes by rule."}, []string{"alertname"}),
		Nested: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_folded_total",
			Help: "Groups folded into a parent incident's thread."}, []string{"alertname"}),
		Escalations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_escalations_total",
			Help: "Pages escalated to the person on call."}, []string{"alertname"}),
		Refreshes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_refreshes_total",
			Help: "Redraws of a firing incident, by whether the message changed."}, []string{"result"}),
		Enrichment: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_enrichment_total",
			Help: "Enrichment lookups by kind and result."}, []string{"kind", "result"}),
		Degraded: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fogbell_degraded_total",
			Help: "Times a feature fell back because Slack or an upstream refused."}, []string{"feature"}),
		HistoryFailed: prometheus.NewCounter(prometheus.CounterOpts{Name: "fogbell_history_unavailable_total",
			Help: "Channel history reads that failed; incidents are then known only from memory."}),
	}
	r.MustRegister(m.SlackRequests, m.Notifications, m.LastSuccess, m.Interactions, m.Interactive, m.Incidents,
		m.AckSeconds, m.ResolveSecs, m.Noise, m.Nested, m.Escalations, m.Refreshes, m.Enrichment, m.Degraded,
		m.HistoryFailed, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	for _, a := range []string{"rejected", "error"} {
		m.Interactions.WithLabelValues(a)
	}
	m.Notifications.WithLabelValues("error")
	return m
}

// Enriched counts one enrichment lookup.
func (m *Metrics) Enriched(kind, result string) {
	m.Enrichment.WithLabelValues(kind, result).Inc()
}
