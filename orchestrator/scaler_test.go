package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeCloud implements CloudProvider for testing.
type fakeCloud struct {
	mu          sync.Mutex
	nextID      int
	created     []int
	destroyed   []int
	bootDelay   time.Duration
	failCreate  bool
	failDestroy bool
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{nextID: 1000}
}

func (f *fakeCloud) CreateGPUNode(_ context.Context, _ string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate {
		return 0, fmt.Errorf("simulated create failure")
	}
	id := f.nextID
	f.nextID++
	f.created = append(f.created, id)
	return id, nil
}

func (f *fakeCloud) WaitForRunning(ctx context.Context, id int, _ time.Duration) (string, error) {
	if f.bootDelay > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(f.bootDelay):
		}
	}
	return fmt.Sprintf("10.0.0.%d", id%256), nil
}

func (f *fakeCloud) DestroyNode(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDestroy {
		return fmt.Errorf("simulated destroy failure")
	}
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *fakeCloud) ListManagedNodes(_ context.Context) ([]CloudNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	destroyed := make(map[int]struct{}, len(f.destroyed))
	for _, id := range f.destroyed {
		destroyed[id] = struct{}{}
	}

	nodes := make([]CloudNode, 0, len(f.created))
	for _, id := range f.created {
		if _, wasDestroyed := destroyed[id]; wasDestroyed {
			continue
		}
		nodes = append(nodes, CloudNode{
			LinodeID: id,
			Label:    fmt.Sprintf("gpu-node-%d", id),
			IPv4:     fmt.Sprintf("10.0.0.%d", id%256),
			Status:   "running",
		})
	}
	return nodes, nil
}

func newTestOrchestrator(cloud CloudProvider) *Orchestrator {
	cfg := Config{
		MinNodes:              0,
		MaxNodes:              2,
		ScaleUpThreshold:      10,
		ScaleUpDuration:       0, // immediate for tests
		CooldownDuration:      0, // no cooldown for tests
		PrometheusTargetsFile: "/tmp/test_gpu_targets.json",
	}
	return NewOrchestrator(cfg, cloud)
}

func TestScaleUp_ZeroToOne(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)

	err := orch.ScaleUp(context.Background())
	if err != nil {
		t.Fatalf("ScaleUp failed: %v", err)
	}

	if len(cloud.created) != 1 {
		t.Fatalf("expected 1 node created, got %d", len(cloud.created))
	}
	if orch.nodes.ActiveCount() != 1 {
		t.Fatalf("expected 1 active node, got %d", orch.nodes.ActiveCount())
	}
}

func TestScaleUp_RespectsMaxCap(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)

	orch.ScaleUp(context.Background())
	orch.ScaleUp(context.Background())
	err := orch.ScaleUp(context.Background())

	if err == nil {
		t.Fatal("expected error when exceeding max node cap")
	}
	if len(cloud.created) != 2 {
		t.Fatalf("expected 2 nodes created, got %d", len(cloud.created))
	}
}

func TestScaleUp_CreateFailure(t *testing.T) {
	cloud := newFakeCloud()
	cloud.failCreate = true
	orch := newTestOrchestrator(cloud)

	err := orch.ScaleUp(context.Background())
	if err == nil {
		t.Fatal("expected error on create failure")
	}
	if orch.nodes.ActiveCount() != 0 {
		t.Fatalf("expected 0 nodes after failed create, got %d", orch.nodes.ActiveCount())
	}
}

func TestScaleDown(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)

	orch.ScaleUp(context.Background())
	id := cloud.created[0]

	// Transition to ready so we can drain
	orch.nodes.TransitionNode(id, StateReady)

	err := orch.ScaleDown(context.Background(), id)
	if err != nil {
		t.Fatalf("ScaleDown failed: %v", err)
	}

	if orch.nodes.ActiveCount() != 0 {
		t.Fatalf("expected 0 active nodes after scale-down, got %d", orch.nodes.ActiveCount())
	}
	if len(cloud.destroyed) != 1 || cloud.destroyed[0] != id {
		t.Fatalf("expected node %d destroyed, got %v", id, cloud.destroyed)
	}
}

func TestScaleDown_DrainLock(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)

	orch.ScaleUp(context.Background())
	id := cloud.created[0]
	orch.nodes.TransitionNode(id, StateReady)
	orch.nodes.SetDrainLock(id, true)

	err := orch.ScaleDown(context.Background(), id)
	if err == nil {
		t.Fatal("expected error when drain lock is active")
	}
	if orch.nodes.ActiveCount() != 1 {
		t.Fatal("node should still be active when drain-locked")
	}
}

func TestScaleDown_NotFound(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)

	err := orch.ScaleDown(context.Background(), 99999)
	if err == nil {
		t.Fatal("expected error for non-existent node")
	}
}

func TestScaleDown_RespectsMinNodes(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)
	orch.cfg.MinNodes = 1

	orch.ScaleUp(context.Background())
	id := cloud.created[0]
	orch.nodes.TransitionNode(id, StateReady)

	err := orch.ScaleDown(context.Background(), id)
	if err == nil {
		t.Fatal("expected scale-down to be blocked by min node floor")
	}
	if orch.nodes.ActiveCount() != 1 {
		t.Fatalf("expected node to remain active, got %d", orch.nodes.ActiveCount())
	}
}

func TestCooldown(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)
	orch.cfg.CooldownDuration = 1 * time.Hour // long cooldown

	orch.ScaleUp(context.Background())

	if orch.CanScale() {
		t.Fatal("should not be able to scale during cooldown")
	}
}

func TestCooldown_Expired(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)
	orch.cfg.CooldownDuration = 0

	orch.ScaleUp(context.Background())

	if !orch.CanScale() {
		t.Fatal("should be able to scale when cooldown is 0")
	}
}

func TestEvaluateScaleUp_ZeroToOne(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)

	orch.EvaluateScaleUp(context.Background(), 5)

	// Give the goroutine time to execute
	time.Sleep(50 * time.Millisecond)

	if len(cloud.created) != 1 {
		t.Fatalf("expected scale 0→1 to create a node, got %d created", len(cloud.created))
	}
}

func TestEvaluateScaleUp_NoActionWhenIdle(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)

	orch.EvaluateScaleUp(context.Background(), 0)

	time.Sleep(50 * time.Millisecond)

	if len(cloud.created) != 0 {
		t.Fatalf("expected no scale action with 0 pending, got %d created", len(cloud.created))
	}
}

func TestEvaluateScaleUp_GeneralizedBeyondTwoNodes(t *testing.T) {
	cloud := newFakeCloud()
	cfg := Config{
		MinNodes:              0,
		MaxNodes:              3,
		ScaleUpThreshold:      1,
		ScaleUpDuration:       0,
		CooldownDuration:      0,
		PrometheusTargetsFile: "/tmp/test_gpu_targets.json",
	}
	orch := NewOrchestrator(cfg, cloud)

	// Bring to 2 ready nodes first.
	orch.ScaleUp(context.Background())
	orch.ScaleUp(context.Background())
	for _, id := range cloud.created {
		orch.nodes.TransitionNode(id, StateReady)
	}

	orch.EvaluateScaleUp(context.Background(), 5)
	orch.EvaluateScaleUp(context.Background(), 5)
	time.Sleep(100 * time.Millisecond)

	if len(cloud.created) != 3 {
		t.Fatalf("expected scale-up from 2->3, got %d created", len(cloud.created))
	}
}

func TestEvaluateScaleDown_TriggersOnIdle(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)
	orch.cfg.ScaleDownIdleDuration = 0
	orch.cfg.ScaleToZeroIdleDuration = 0

	orch.ScaleUp(context.Background())
	id := cloud.created[0]
	orch.nodes.TransitionNode(id, StateReady)

	orch.EvaluateScaleDown(context.Background(), 0)
	time.Sleep(100 * time.Millisecond)

	if orch.nodes.ActiveCount() != 0 {
		t.Fatalf("expected scale-down on idle to remove node, got %d active", orch.nodes.ActiveCount())
	}
}

func TestEvaluateScaleDown_DoesNotTriggerWhenWorkExists(t *testing.T) {
	cloud := newFakeCloud()
	orch := newTestOrchestrator(cloud)
	orch.cfg.ScaleDownIdleDuration = 0
	orch.cfg.ScaleToZeroIdleDuration = 0

	orch.ScaleUp(context.Background())
	id := cloud.created[0]
	orch.nodes.TransitionNode(id, StateReady)

	orch.EvaluateScaleDown(context.Background(), 1)
	time.Sleep(100 * time.Millisecond)

	if orch.nodes.ActiveCount() != 1 {
		t.Fatalf("expected no scale-down when total work > 0, got %d active", orch.nodes.ActiveCount())
	}
}

func TestWaitForNodeReady(t *testing.T) {
	cloud := newFakeCloud()
	cloud.bootDelay = 10 * time.Millisecond
	orch := newTestOrchestrator(cloud)

	orch.ScaleUp(context.Background())
	id := cloud.created[0]

	// waitForNodeReady runs in background goroutine — wait for it
	time.Sleep(100 * time.Millisecond)

	node, exists := orch.nodes.GetNode(id)
	if !exists {
		t.Fatal("node should exist")
	}
	if node.State != StateReady {
		t.Fatalf("expected node state READY, got %s", node.State)
	}
	if node.IPv4 == "" {
		t.Fatal("expected node to have an IP address")
	}
}
