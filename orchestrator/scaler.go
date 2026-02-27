package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// CanScale checks if the cooldown period has elapsed since the last scale event.
func (o *Orchestrator) CanScale() bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.lastScaleEvent.IsZero() {
		return true
	}

	elapsed := time.Since(o.lastScaleEvent)
	canScale := elapsed >= o.cfg.CooldownDuration

	if !canScale {
		o.metrics.CooldownActive.Set(1)
	} else {
		o.metrics.CooldownActive.Set(0)
	}

	return canScale
}

// recordScaleEvent marks the current time as the last scale event (for cooldown).
func (o *Orchestrator) recordScaleEvent() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastScaleEvent = time.Now()
}

// EvaluateScaleUp checks queue depth and decides whether to scale up.
// Called on every monitor loop tick.
func (o *Orchestrator) EvaluateScaleUp(ctx context.Context, pending int) {
	activeCount := o.nodes.ActiveCount()
	readyCount := o.nodes.ReadyCount()

	// --- Scale 0→1: Any pending messages with no active nodes ---
	if pending > 0 && activeCount == 0 {
		if !o.CanScale() {
			slog.Info("scale 0→1 blocked by cooldown")
			return
		}
		if o.nodes.IsScalingInProgress() {
			slog.Debug("scale-up already in progress")
			return
		}

		slog.Info("triggering scale 0→1", "pending", pending)
		go func() {
			if err := o.ScaleUp(ctx); err != nil {
				slog.Error("scale 0→1 failed", "error", err)
			}
		}()
		return
	}

	// --- Scale N→N+1 (where N>=1): Queue depth exceeds threshold for sustained period ---
	if pending > o.cfg.ScaleUpThreshold && readyCount == activeCount && activeCount > 0 && activeCount < o.cfg.MaxNodes {
		now := time.Now()

		o.mu.Lock()
		if o.scaleUpSince == nil {
			o.scaleUpSince = &now
			o.mu.Unlock()
			slog.Info("queue depth exceeded threshold, starting timer",
				"total", pending,
				"threshold", o.cfg.ScaleUpThreshold,
				"active_nodes", activeCount,
			)
			return
		}
		elapsed := now.Sub(*o.scaleUpSince)
		o.mu.Unlock()

		if elapsed >= o.cfg.ScaleUpDuration {
			if !o.CanScale() {
				slog.Info("scale-up blocked by cooldown")
				return
			}
			if o.nodes.IsScalingInProgress() {
				slog.Debug("scale-up already in progress")
				return
			}

			slog.Info("triggering scale-up",
				"total", pending,
				"from", activeCount,
				"to", activeCount+1,
				"sustained_for", elapsed,
			)

			o.mu.Lock()
			o.scaleUpSince = nil
			o.mu.Unlock()

			go func() {
				if err := o.ScaleUp(ctx); err != nil {
					slog.Error("scale-up failed", "error", err)
				}
			}()
		}
		return
	}

	// Reset scale-up timer if queue depth drops below threshold
	o.mu.Lock()
	if o.scaleUpSince != nil && pending <= o.cfg.ScaleUpThreshold {
		o.scaleUpSince = nil
		slog.Debug("queue depth dropped below threshold, resetting timer")
	}
	o.mu.Unlock()
}

// EvaluateScaleDown checks request-idle time and decides whether to scale down.
// Called on every monitor loop tick.
func (o *Orchestrator) EvaluateScaleDown(ctx context.Context, total int) {
	activeCount := o.nodes.ActiveCount()
	readyNodes := o.nodes.NodesByState(StateReady)
	if activeCount == 0 || len(readyNodes) == 0 || activeCount <= o.cfg.MinNodes {
		o.mu.Lock()
		o.scaleDownSince = nil
		o.mu.Unlock()
		return
	}

	// Any outstanding work means we are not idle.
	if total > 0 {
		o.mu.Lock()
		if o.scaleDownSince != nil {
			slog.Debug("queue total above zero, resetting scale-down idle timer",
				"total", total,
			)
		}
		o.scaleDownSince = nil
		o.mu.Unlock()
		return
	}

	now := time.Now()
	o.mu.Lock()
	if o.scaleDownSince == nil {
		o.scaleDownSince = &now
	}
	elapsed := now.Sub(*o.scaleDownSince)
	o.mu.Unlock()

	var idleRequired time.Duration
	if activeCount > 1 {
		idleRequired = o.cfg.ScaleDownIdleDuration
	} else {
		idleRequired = o.cfg.ScaleToZeroIdleDuration
	}

	if elapsed < idleRequired {
		return
	}
	if !o.CanScale() {
		slog.Info("scale-down blocked by cooldown")
		return
	}
	if o.nodes.IsScalingInProgress() {
		slog.Debug("scale operation already in progress")
		return
	}

	targetID := pickScaleDownTarget(readyNodes)
	if targetID == 0 {
		return
	}

	slog.Info("triggering request-idle scale-down",
		"total", total,
		"active_nodes", activeCount,
		"idle_for", elapsed,
		"idle_required", idleRequired,
		"linode_id", targetID,
	)

	// Reset the idle timer so consecutive scale-down events require a fresh idle window.
	o.mu.Lock()
	o.scaleDownSince = nil
	o.mu.Unlock()

	go func(id int) {
		if err := o.ScaleDown(ctx, id); err != nil {
			slog.Error("request-idle scale-down failed", "linode_id", id, "error", err)
		}
	}(targetID)
}

func pickScaleDownTarget(nodes []*Node) int {
	if len(nodes) == 0 {
		return 0
	}
	target := nodes[0].LinodeID
	for _, n := range nodes[1:] {
		// Prefer the highest Linode ID (typically newest node first).
		if n.LinodeID > target {
			target = n.LinodeID
		}
	}
	return target
}

// ScaleUp provisions a new GPU node via the Linode API.
func (o *Orchestrator) ScaleUp(ctx context.Context) error {
	activeCount := o.nodes.ActiveCount()
	if activeCount >= o.cfg.MaxNodes {
		return fmt.Errorf("max node cap (%d) reached", o.cfg.MaxNodes)
	}

	from := fmt.Sprintf("%d", activeCount)
	to := fmt.Sprintf("%d", activeCount+1)
	label := fmt.Sprintf("gpu-node-%d-%d", activeCount+1, time.Now().Unix())

	// Create instance via cloud provider
	linodeID, err := o.linode.CreateGPUNode(ctx, label)
	if err != nil {
		return fmt.Errorf("failed to create GPU node: %w", err)
	}

	// Track node in state machine
	node, err := o.nodes.AddNode(linodeID, label)
	if err != nil {
		_ = o.linode.DestroyNode(ctx, linodeID)
		return fmt.Errorf("failed to track node: %w", err)
	}

	o.recordScaleEvent()
	o.metrics.RecordScaleEvent("up", from, to)
	o.metrics.UpdateNodeCounts(o.nodes)

	// Wait for node to become ready in background
	go o.waitForNodeReady(ctx, node)

	return nil
}

// waitForNodeReady polls until the Linode instance is running, then transitions to READY.
func (o *Orchestrator) waitForNodeReady(ctx context.Context, node *Node) {
	ip, err := o.linode.WaitForRunning(ctx, node.LinodeID, 10*time.Minute)
	if err != nil {
		slog.Error("node failed to reach running state",
			"linode_id", node.LinodeID,
			"error", err,
		)
		// Clean up failed node
		_ = o.nodes.TransitionNode(node.LinodeID, StateDestroying)
		_ = o.linode.DestroyNode(ctx, node.LinodeID)
		o.nodes.RemoveNode(node.LinodeID)
		o.metrics.UpdateNodeCounts(o.nodes)
		return
	}

	// Update node IP
	if err := o.nodes.SetNodeIP(node.LinodeID, ip); err != nil {
		slog.Error("failed to set node IP", "linode_id", node.LinodeID, "error", err)
		return
	}

	// Transition to READY
	if err := o.nodes.TransitionNode(node.LinodeID, StateReady); err != nil {
		slog.Error("failed to transition node to ready", "linode_id", node.LinodeID, "error", err)
		return
	}

	// Record provisioning latency
	latency := time.Since(node.CreatedAt).Seconds()
	o.metrics.ProvisioningLatency.Observe(latency)
	o.metrics.UpdateNodeCounts(o.nodes)

	slog.Info("node is ready",
		"linode_id", node.LinodeID,
		"ip", ip,
		"provisioning_seconds", latency,
	)

	// Update Prometheus targets so it discovers the new node
	if err := WriteTargetsFile(o.cfg.PrometheusTargetsFile, o.nodes); err != nil {
		slog.Error("failed to update prometheus targets", "error", err)
	}
}

// ScaleDown drains and destroys a GPU node.
func (o *Orchestrator) ScaleDown(ctx context.Context, linodeID int) error {
	node, exists := o.nodes.GetNode(linodeID)
	if !exists {
		return fmt.Errorf("node %d not found", linodeID)
	}

	// Respect drain lock (workload-requested "don't kill me")
	if node.DrainLocked {
		slog.Info("scale-down blocked by drain lock", "linode_id", linodeID)
		return fmt.Errorf("node %d has drain lock active", linodeID)
	}

	if !o.CanScale() {
		return fmt.Errorf("scale-down blocked by cooldown")
	}

	activeCount := o.nodes.ActiveCount()
	if activeCount <= o.cfg.MinNodes {
		return fmt.Errorf("scale-down blocked by min node floor (%d)", o.cfg.MinNodes)
	}
	from := fmt.Sprintf("%d", activeCount)
	to := fmt.Sprintf("%d", activeCount-1)

	// Transition to DRAINING
	if err := o.nodes.TransitionNode(linodeID, StateDraining); err != nil {
		return fmt.Errorf("failed to transition to draining: %w", err)
	}
	o.metrics.UpdateNodeCounts(o.nodes)

	slog.Info("draining node", "linode_id", linodeID, "ip", node.IPv4)

	// Phase 1: fire-and-forget — no in-flight work to wait on.
	// In future phases, signal the NATS agent to unsubscribe and wait for completion.

	// Transition to DESTROYING
	if err := o.nodes.TransitionNode(linodeID, StateDestroying); err != nil {
		return fmt.Errorf("failed to transition to destroying: %w", err)
	}
	o.metrics.UpdateNodeCounts(o.nodes)

	// Destroy via Linode API
	if err := o.linode.DestroyNode(ctx, linodeID); err != nil {
		return fmt.Errorf("failed to destroy node: %w", err)
	}

	// Remove from tracking
	o.nodes.RemoveNode(linodeID)
	o.recordScaleEvent()
	o.metrics.RecordScaleEvent("down", from, to)
	o.metrics.UpdateNodeCounts(o.nodes)

	slog.Info("node destroyed", "linode_id", linodeID)

	// Update Prometheus targets so it stops scraping the destroyed node
	if err := WriteTargetsFile(o.cfg.PrometheusTargetsFile, o.nodes); err != nil {
		slog.Error("failed to update prometheus targets", "error", err)
	}

	return nil
}
