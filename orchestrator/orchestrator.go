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
	DLQStream    string
	DLQSubject   string

	// Linode node identity tags
	LinodeManagedTag string
	LinodeClusterTag string

	// Scaling thresholds
	MinNodes         int
	MaxNodes         int
	ScaleUpThreshold int           // pending messages for 1→2
	ScaleUpDuration  time.Duration // how long threshold must be exceeded
	CooldownDuration time.Duration // minimum interval between scale events
	ScaleDownIdleDuration   time.Duration // idle time required for 2→1
	ScaleToZeroIdleDuration time.Duration // idle time required for 1→0

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
	scaleDownSince *time.Time // when queue total first became 0
}

type QueueStats struct {
	Queued   int
	Inflight int
}

func (q QueueStats) Total() int {
	return q.Queued + q.Inflight
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

	// Reconcile local state from Linode API at startup.
	if err := o.reconcileWithCloud(ctx); err != nil {
		slog.Warn("initial cloud reconciliation failed", "error", err)
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
	if o.cfg.DLQStream == "" {
		return fmt.Errorf("dlq stream name is required")
	}
	if o.cfg.DLQSubject == "" {
		return fmt.Errorf("dlq subject is required")
	}

	if err := o.ensureStream(o.cfg.StreamName, []string{o.cfg.StreamName}, nats.WorkQueuePolicy); err != nil {
		return err
	}
	return o.ensureStream(o.cfg.DLQStream, []string{o.cfg.DLQSubject}, nats.LimitsPolicy)
}

func (o *Orchestrator) ensureStream(name string, subjects []string, retention nats.RetentionPolicy) error {
	if si, err := o.js.StreamInfo(name); err == nil {
		needsUpdate := si.Config.Retention != retention || !sameSubjects(si.Config.Subjects, subjects)
		if !needsUpdate {
			slog.Info("JetStream stream already exists", "stream", name)
			return nil
		}

		slog.Info("JetStream stream exists with different config, updating",
			"stream", name,
			"old_retention", si.Config.Retention.String(),
			"new_retention", retention.String(),
			"old_subjects", si.Config.Subjects,
			"new_subjects", subjects,
		)
		if _, updateErr := o.js.UpdateStream(&nats.StreamConfig{
			Name:      name,
			Subjects:  subjects,
			Storage:   nats.FileStorage,
			Retention: retention,
		}); updateErr != nil {
			return fmt.Errorf("update stream %q: %w", name, updateErr)
		}
		return nil
	}

	slog.Info("JetStream stream missing, creating", "stream", name, "subjects", subjects)
	_, err := o.js.AddStream(&nats.StreamConfig{
		Name:      name,
		Subjects:  subjects,
		Storage:   nats.FileStorage,
		Retention: retention,
	})
	if err == nil {
		slog.Info("JetStream stream created", "stream", name)
		return nil
	}

	// Handle startup races where another instance creates the stream first.
	if _, infoErr := o.js.StreamInfo(name); infoErr == nil {
		slog.Info("JetStream stream became available during create", "stream", name)
		return nil
	}

	return fmt.Errorf("create stream %q: %w", name, err)
}

func sameSubjects(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
			if err := o.reconcileWithCloud(ctx); err != nil {
				slog.Error("failed to reconcile cloud node state", "error", err)
				continue
			}

			queue, err := o.getQueueStats()
			if err != nil {
				slog.Error("failed to get queue stats", "error", err)
				continue
			}

			o.metrics.QueuePending.Set(float64(queue.Queued))
			o.metrics.QueueAckPending.Set(float64(queue.Inflight))
			o.metrics.QueueDepth.Set(float64(queue.Total()))
			o.metrics.UpdateNodeCounts(o.nodes)

			slog.Debug("queue status",
				"total", queue.Total(),
				"inflight", queue.Inflight,
				"active_nodes", o.nodes.ActiveCount(),
				"ready_nodes", o.nodes.ReadyCount(),
			)

			o.EvaluateScaleUp(ctx, queue.Total())
			o.EvaluateScaleDown(ctx, queue.Total())
		}
	}
}

// reconcileWithCloud syncs in-memory node state from Linode API.
// Linode is treated as the source of truth for managed node existence and runtime status.
func (o *Orchestrator) reconcileWithCloud(ctx context.Context) error {
	cloudNodes, err := o.linode.ListManagedNodes(ctx)
	if err != nil {
		return err
	}

	seen := make(map[int]struct{}, len(cloudNodes))
	changed := false

	for _, cn := range cloudNodes {
		seen[cn.LinodeID] = struct{}{}

		node, exists := o.nodes.GetNode(cn.LinodeID)
		if !exists {
			if _, addErr := o.nodes.AddNode(cn.LinodeID, cn.Label); addErr != nil {
				slog.Warn("failed to add cloud node to state manager",
					"linode_id", cn.LinodeID,
					"label", cn.Label,
					"error", addErr,
				)
				continue
			}
			changed = true
			node, _ = o.nodes.GetNode(cn.LinodeID)
		}

		if cn.IPv4 != "" && node.IPv4 != cn.IPv4 {
			if setErr := o.nodes.SetNodeIP(cn.LinodeID, cn.IPv4); setErr != nil {
				slog.Warn("failed to update node IP from cloud state",
					"linode_id", cn.LinodeID,
					"error", setErr,
				)
			} else {
				changed = true
			}
		}

		if isCloudNodeRunning(cn.Status) && node.State == StateProvisioning {
			if trErr := o.nodes.TransitionNode(cn.LinodeID, StateReady); trErr != nil {
				slog.Warn("failed to transition node to ready from cloud state",
					"linode_id", cn.LinodeID,
					"error", trErr,
				)
			} else {
				changed = true
			}
		}

		if !isCloudNodeRunning(cn.Status) && node.State == StateReady {
			slog.Warn("tracked ready node is not running in Linode API",
				"linode_id", cn.LinodeID,
				"status", cn.Status,
			)
		}
	}

	for _, local := range o.nodes.AllNodes() {
		if _, ok := seen[local.LinodeID]; ok {
			continue
		}
		// Do not eagerly remove fresh provisioning nodes to avoid race with eventual consistency.
		if local.State == StateProvisioning {
			continue
		}
		o.nodes.RemoveNode(local.LinodeID)
		changed = true
		slog.Info("removed stale node from local state", "linode_id", local.LinodeID)
	}

	if changed {
		o.metrics.UpdateNodeCounts(o.nodes)
		if err := WriteTargetsFile(o.cfg.PrometheusTargetsFile, o.nodes); err != nil {
			slog.Warn("failed to write targets file after reconciliation", "error", err)
		}
	}

	return nil
}

func isCloudNodeRunning(status string) bool {
	return status == "running"
}

// getQueueStats returns queue stats from JetStream.
// Queued = not-yet-delivered; Inflight = delivered but unacknowledged.
// Falls back to stream state when no consumer info is available.
func (o *Orchestrator) getQueueStats() (QueueStats, error) {
	// Try consumer info first (most accurate when consumers exist)
	ci, err := o.js.ConsumerInfo(o.cfg.StreamName, o.cfg.ConsumerName)
	if err == nil {
		return QueueStats{
			Queued:   int(ci.NumPending),
			Inflight: int(ci.NumAckPending),
		}, nil
	}

	// Fallback to stream info only for work-queue streams where Msgs reflects
	// unprocessed queue depth when no consumer is currently attached.
	si, err := o.js.StreamInfo(o.cfg.StreamName)
	if err != nil {
		// Stream may not exist yet — that's fine, no pending messages
		return QueueStats{}, nil
	}
	if si.Config.Retention != nats.WorkQueuePolicy {
		// For non-workqueue streams, Msgs can include already-processed history
		// and is not a reliable "pending jobs" metric.
		return QueueStats{}, nil
	}

	return QueueStats{
		Queued:   int(si.State.Msgs),
		Inflight: 0,
	}, nil
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
