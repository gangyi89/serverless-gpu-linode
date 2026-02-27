package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds the orchestrator configuration loaded from environment variables.
type Config struct {
	// NATS connection
	NatsURL      string
	StreamName   string
	ConsumerName string

	// Scaling thresholds
	MaxNodes         int
	ScaleUpThreshold int           // pending messages for 1→2
	ScaleUpDuration  time.Duration // how long threshold must be exceeded
	CooldownDuration time.Duration // minimum interval between scale events

	// Monitoring
	PrometheusTargetsFile string
	ListenAddr            string

	// Polling
	MonitorInterval time.Duration
}

// Orchestrator is the core scaling controller. It monitors NATS queue depth,
// manages GPU node lifecycle via the Linode API, and exposes metrics + alert endpoints.
type Orchestrator struct {
	cfg   Config
	mu    sync.Mutex
	nodes *NodeManager

	nc     *nats.Conn
	js     nats.JetStreamContext
	linode CloudProvider

	metrics        *Metrics
	lastScaleEvent time.Time
	scaleUpSince   *time.Time // when pending first exceeded threshold (for 1→2)
}

// NewOrchestrator creates an Orchestrator with the given configuration and cloud provider.
func NewOrchestrator(cfg Config, cloud CloudProvider) *Orchestrator {
	return &Orchestrator{
		cfg:     cfg,
		nodes:   NewNodeManager(cfg.MaxNodes),
		linode:  cloud,
		metrics: NewMetrics(),
	}
}

// Run starts the orchestrator: connects to NATS, starts the HTTP server, and begins monitoring.
// Blocks until the context is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
	// Connect to NATS
	if err := o.connectNATS(); err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}
	defer o.nc.Close()

	// Ensure required JetStream resources exist before monitoring.
	if err := o.ensureJetStreamResources(); err != nil {
		return fmt.Errorf("failed to ensure JetStream resources: %w", err)
	}

	// Write initial empty targets file
	if err := WriteTargetsFile(o.cfg.PrometheusTargetsFile, o.nodes); err != nil {
		slog.Warn("failed to write initial targets file", "error", err)
	}

	// Start HTTP server for /alerts and /metrics
	go o.startHTTPServer()

	// Start monitoring loop (blocks until ctx is done)
	o.monitorLoop(ctx)

	return nil
}

// connectNATS establishes a connection to NATS with JetStream enabled.
func (o *Orchestrator) connectNATS() error {
	slog.Info("connecting to NATS", "url", o.cfg.NatsURL)

	nc, err := nats.Connect(o.cfg.NatsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			slog.Info("NATS reconnected")
		}),
	)
	if err != nil {
		return err
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("failed to create JetStream context: %w", err)
	}

	o.nc = nc
	o.js = js

	slog.Info("connected to NATS with JetStream")
	return nil
}

// ensureJetStreamResources guarantees the GPU jobs stream exists.
// It is safe to call on every startup.
func (o *Orchestrator) ensureJetStreamResources() error {
	if o.cfg.StreamName == "" {
		return fmt.Errorf("stream name is required")
	}

	if _, err := o.js.StreamInfo(o.cfg.StreamName); err == nil {
		slog.Info("JetStream stream already exists", "stream", o.cfg.StreamName)
		return nil
	}

	slog.Info("JetStream stream missing, creating", "stream", o.cfg.StreamName)
	_, err := o.js.AddStream(&nats.StreamConfig{
		Name:      o.cfg.StreamName,
		Subjects:  []string{o.cfg.StreamName},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
	})
	if err == nil {
		slog.Info("JetStream stream created", "stream", o.cfg.StreamName)
		return nil
	}

	// Handle startup races where another instance creates the stream first.
	if _, infoErr := o.js.StreamInfo(o.cfg.StreamName); infoErr == nil {
		slog.Info("JetStream stream became available during create", "stream", o.cfg.StreamName)
		return nil
	}

	return fmt.Errorf("create stream %q: %w", o.cfg.StreamName, err)
}

// monitorLoop periodically checks NATS queue depth and evaluates scale-up conditions.
func (o *Orchestrator) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(o.cfg.MonitorInterval)
	defer ticker.Stop()

	slog.Info("starting monitor loop", "interval", o.cfg.MonitorInterval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("monitor loop stopped")
			return
		case <-ticker.C:
			pending, err := o.getPendingCount()
			if err != nil {
				slog.Error("failed to get pending count", "error", err)
				continue
			}

			o.metrics.QueueDepth.Set(float64(pending))
			o.metrics.UpdateNodeCounts(o.nodes)

			slog.Debug("queue status",
				"pending", pending,
				"active_nodes", o.nodes.ActiveCount(),
				"ready_nodes", o.nodes.ReadyCount(),
			)

			o.EvaluateScaleUp(ctx, pending)
		}
	}
}

// getPendingCount returns the number of pending messages in the GPU jobs stream.
// Falls back from consumer info to stream info when no consumers exist (0 nodes).
func (o *Orchestrator) getPendingCount() (int, error) {
	// Try consumer info first (most accurate when consumers exist)
	ci, err := o.js.ConsumerInfo(o.cfg.StreamName, o.cfg.ConsumerName)
	if err == nil {
		return int(ci.NumPending), nil
	}

	// Fallback to stream info (when no consumers exist, e.g. 0 GPU nodes)
	si, err := o.js.StreamInfo(o.cfg.StreamName)
	if err != nil {
		// Stream may not exist yet — that's fine, no pending messages
		return 0, nil
	}

	return int(si.State.Msgs), nil
}

// startHTTPServer starts the HTTP server for alerts webhook, metrics, and health check.
func (o *Orchestrator) startHTTPServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/alerts", o.HandleAlerts)
	mux.Handle("/metrics", promhttp.HandlerFor(o.metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	slog.Info("starting HTTP server", "addr", o.cfg.ListenAddr)
	if err := http.ListenAndServe(o.cfg.ListenAddr, mux); err != nil {
		slog.Error("HTTP server failed", "error", err)
	}
}
