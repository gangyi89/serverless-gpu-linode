package main

import (
	"testing"
)

func TestAddNode(t *testing.T) {
	nm := NewNodeManager(2)

	node, err := nm.AddNode(100, "gpu-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node.State != StateProvisioning {
		t.Fatalf("expected state %s, got %s", StateProvisioning, node.State)
	}
	if nm.ActiveCount() != 1 {
		t.Fatalf("expected 1 active node, got %d", nm.ActiveCount())
	}
}

func TestAddNodeDuplicate(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")

	_, err := nm.AddNode(100, "gpu-1-dup")
	if err == nil {
		t.Fatal("expected error for duplicate node")
	}
}

func TestAddNodeMaxCap(t *testing.T) {
	nm := NewNodeManager(1)
	nm.AddNode(100, "gpu-1")

	_, err := nm.AddNode(200, "gpu-2")
	if err == nil {
		t.Fatal("expected error when exceeding max node cap")
	}
}

func TestValidTransitions(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")

	// provisioning → ready
	if err := nm.TransitionNode(100, StateReady); err != nil {
		t.Fatalf("provisioning → ready failed: %v", err)
	}

	// ready → draining
	if err := nm.TransitionNode(100, StateDraining); err != nil {
		t.Fatalf("ready → draining failed: %v", err)
	}

	// draining → destroying
	if err := nm.TransitionNode(100, StateDestroying); err != nil {
		t.Fatalf("draining → destroying failed: %v", err)
	}
}

func TestInvalidTransition(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")

	// provisioning → draining (not allowed)
	if err := nm.TransitionNode(100, StateDraining); err == nil {
		t.Fatal("expected error for invalid transition provisioning → draining")
	}
}

func TestTransitionProvisioningToDestroying(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")

	// provisioning → destroying (allowed — for cleanup on failed provision)
	if err := nm.TransitionNode(100, StateDestroying); err != nil {
		t.Fatalf("provisioning → destroying should be allowed: %v", err)
	}
}

func TestRemoveNode(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")
	nm.RemoveNode(100)

	if nm.ActiveCount() != 0 {
		t.Fatalf("expected 0 nodes after removal, got %d", nm.ActiveCount())
	}
}

func TestNodesByState(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")
	nm.AddNode(200, "gpu-2")
	nm.TransitionNode(200, StateReady)

	provisioning := nm.NodesByState(StateProvisioning)
	if len(provisioning) != 1 {
		t.Fatalf("expected 1 provisioning node, got %d", len(provisioning))
	}

	ready := nm.NodesByState(StateReady)
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready node, got %d", len(ready))
	}
}

func TestActiveCountExcludesDestroying(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")
	nm.TransitionNode(100, StateReady)
	nm.TransitionNode(100, StateDraining)
	nm.TransitionNode(100, StateDestroying)

	if nm.ActiveCount() != 0 {
		t.Fatalf("expected 0 active (destroying excluded), got %d", nm.ActiveCount())
	}
}

func TestFindNodeByIP(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")
	nm.SetNodeIP(100, "10.0.0.1")

	node, found := nm.FindNodeByIP("10.0.0.1")
	if !found {
		t.Fatal("expected to find node by IP")
	}
	if node.LinodeID != 100 {
		t.Fatalf("expected linode ID 100, got %d", node.LinodeID)
	}

	_, found = nm.FindNodeByIP("10.0.0.99")
	if found {
		t.Fatal("should not find node with unknown IP")
	}
}

func TestIsScalingInProgress(t *testing.T) {
	nm := NewNodeManager(2)

	if nm.IsScalingInProgress() {
		t.Fatal("no nodes, should not be scaling")
	}

	nm.AddNode(100, "gpu-1") // provisioning
	if !nm.IsScalingInProgress() {
		t.Fatal("provisioning node should count as scaling in progress")
	}

	nm.TransitionNode(100, StateReady)
	if nm.IsScalingInProgress() {
		t.Fatal("only ready nodes, should not be scaling in progress")
	}
}

func TestDrainLock(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")

	if err := nm.SetDrainLock(100, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	node, _ := nm.GetNode(100)
	if !node.DrainLocked {
		t.Fatal("expected drain lock to be set")
	}

	nm.SetDrainLock(100, false)
	node, _ = nm.GetNode(100)
	if node.DrainLocked {
		t.Fatal("expected drain lock to be cleared")
	}
}

func TestReadyAt(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")

	node, _ := nm.GetNode(100)
	if !node.ReadyAt.IsZero() {
		t.Fatal("ReadyAt should be zero before transition to ready")
	}

	nm.TransitionNode(100, StateReady)
	node, _ = nm.GetNode(100)
	if node.ReadyAt.IsZero() {
		t.Fatal("ReadyAt should be set after transition to ready")
	}
}
