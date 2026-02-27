package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics exposed by the orchestrator.
type Metrics struct {
	Registry            *prometheus.Registry
	QueueDepth          prometheus.Gauge
	NodeCount           prometheus.Gauge
	NodeCountByState    *prometheus.GaugeVec
	ProvisioningLatency prometheus.Histogram
	CooldownActive      prometheus.Gauge
	ScaleEvents         *prometheus.CounterVec
	LastScaleEventTime  prometheus.Gauge
}

// NewMetrics creates a new Prometheus registry and registers all orchestrator metrics.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)

	return &Metrics{
		Registry: reg,
		QueueDepth: factory.NewGauge(prometheus.GaugeOpts{
			Name: "orchestrator_queue_depth",
			Help: "Current number of pending messages in the GPU jobs stream",
		}),
		NodeCount: factory.NewGauge(prometheus.GaugeOpts{
			Name: "orchestrator_node_count",
			Help: "Current number of active GPU nodes",
		}),
		NodeCountByState: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "orchestrator_node_count_by_state",
			Help: "Number of GPU nodes by lifecycle state",
		}, []string{"state"}),
		ProvisioningLatency: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "orchestrator_provisioning_latency_seconds",
			Help:    "Time from node creation to ready state",
			Buckets: prometheus.LinearBuckets(30, 30, 10),
		}),
		CooldownActive: factory.NewGauge(prometheus.GaugeOpts{
			Name: "orchestrator_cooldown_active",
			Help: "Whether the scaling cooldown is currently active (1=active, 0=inactive)",
		}),
		ScaleEvents: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "orchestrator_scale_events_total",
			Help: "Total number of scaling events",
		}, []string{"direction", "from", "to"}),
		LastScaleEventTime: factory.NewGauge(prometheus.GaugeOpts{
			Name: "orchestrator_last_scale_event_timestamp",
			Help: "Unix timestamp of the last scaling event",
		}),
	}
}

// RecordScaleEvent increments the scale event counter and updates the timestamp.
func (m *Metrics) RecordScaleEvent(direction, from, to string) {
	m.ScaleEvents.WithLabelValues(direction, from, to).Inc()
	m.LastScaleEventTime.SetToCurrentTime()
}

// UpdateNodeCounts refreshes all node count gauges from the current node state.
func (m *Metrics) UpdateNodeCounts(nm *NodeManager) {
	m.NodeCount.Set(float64(nm.ActiveCount()))

	states := []NodeState{StateProvisioning, StateReady, StateDraining, StateDestroying}
	for _, s := range states {
		count := len(nm.NodesByState(s))
		m.NodeCountByState.WithLabelValues(string(s)).Set(float64(count))
	}
}
