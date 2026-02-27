package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	Registry          *prometheus.Registry
	MessagesReceived  prometheus.Counter
	MessagesAcked     prometheus.Counter
	MessagesNacked    prometheus.Counter
	MessagesDLQ       prometheus.Counter
	ForwardFailures   prometheus.Counter
	ForwardLatency    prometheus.Histogram
	InflightMessages  prometheus.Gauge
	LastSuccessUnix   prometheus.Gauge
	LastFailureUnix   prometheus.Gauge
}

func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)

	return &Metrics{
		Registry: reg,
		MessagesReceived: factory.NewCounter(prometheus.CounterOpts{
			Name: "nats_agent_messages_received_total",
			Help: "Total messages received from JetStream",
		}),
		MessagesAcked: factory.NewCounter(prometheus.CounterOpts{
			Name: "nats_agent_messages_acked_total",
			Help: "Total messages acknowledged",
		}),
		MessagesNacked: factory.NewCounter(prometheus.CounterOpts{
			Name: "nats_agent_messages_nacked_total",
			Help: "Total messages negatively acknowledged for retry",
		}),
		MessagesDLQ: factory.NewCounter(prometheus.CounterOpts{
			Name: "nats_agent_messages_dlq_total",
			Help: "Total messages published to DLQ",
		}),
		ForwardFailures: factory.NewCounter(prometheus.CounterOpts{
			Name: "nats_agent_forward_failures_total",
			Help: "Total HTTP forward failures",
		}),
		ForwardLatency: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "nats_agent_forward_latency_seconds",
			Help:    "Latency of forwarding to AI endpoint",
			Buckets: prometheus.DefBuckets,
		}),
		InflightMessages: factory.NewGauge(prometheus.GaugeOpts{
			Name: "nats_agent_inflight_messages",
			Help: "Current number of in-flight message handlers",
		}),
		LastSuccessUnix: factory.NewGauge(prometheus.GaugeOpts{
			Name: "nats_agent_last_success_timestamp",
			Help: "Unix timestamp of the last successful forward",
		}),
		LastFailureUnix: factory.NewGauge(prometheus.GaugeOpts{
			Name: "nats_agent_last_failure_timestamp",
			Help: "Unix timestamp of the last failed forward",
		}),
	}
}
