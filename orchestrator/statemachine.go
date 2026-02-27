package main

import (
	"fmt"
	"sync"
	"time"
)

// NodeState represents the lifecycle state of a GPU node.
type NodeState string

const (
	StateProvisioning NodeState = "provisioning"
	StateReady        NodeState = "ready"
	StateDraining     NodeState = "draining"
	StateDestroying   NodeState = "destroying"
)

// validTransitions defines allowed state transitions.
var validTransitions = map[NodeState][]NodeState{
	StateProvisioning: {StateReady, StateDestroying},
	StateReady:        {StateDraining},
	StateDraining:     {StateDestroying},
	StateDestroying:   {}, // terminal — node removed after destruction
}

// Node represents a GPU VM managed by the orchestrator.
type Node struct {
	LinodeID    int
	Label       string
	IPv4        string
	State       NodeState
	CreatedAt   time.Time
	ReadyAt     time.Time
	DrainLocked bool // workload-requested "don't kill me" flag
}

// NodeManager manages the set of GPU nodes and enforces state transitions.
type NodeManager struct {
	mu       sync.RWMutex
	nodes    map[int]*Node // keyed by Linode instance ID
	maxNodes int
}

// NewNodeManager creates a NodeManager with the given max node cap.
func NewNodeManager(maxNodes int) *NodeManager {
	return &NodeManager{
		nodes:    make(map[int]*Node),
		maxNodes: maxNodes,
	}
}

// AddNode registers a new node in the PROVISIONING state.
func (nm *NodeManager) AddNode(linodeID int, label string) (*Node, error) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if len(nm.nodes) >= nm.maxNodes {
		return nil, fmt.Errorf("max node cap (%d) reached", nm.maxNodes)
	}
	if _, exists := nm.nodes[linodeID]; exists {
		return nil, fmt.Errorf("node %d already tracked", linodeID)
	}

	node := &Node{
		LinodeID:  linodeID,
		Label:     label,
		State:     StateProvisioning,
		CreatedAt: time.Now(),
	}
	nm.nodes[linodeID] = node
	return node, nil
}

// TransitionNode moves a node to a new state if the transition is valid.
func (nm *NodeManager) TransitionNode(linodeID int, to NodeState) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	node, exists := nm.nodes[linodeID]
	if !exists {
		return fmt.Errorf("node %d not found", linodeID)
	}

	// Idempotent transition: repeated attempts to set the same state are no-ops.
	// This avoids noisy "ready -> ready" errors when multiple reconciliation paths
	// observe the same cloud state around the same time.
	if node.State == to {
		if to == StateReady && node.ReadyAt.IsZero() {
			node.ReadyAt = time.Now()
		}
		return nil
	}

	allowed := validTransitions[node.State]
	for _, s := range allowed {
		if s == to {
			node.State = to
			if to == StateReady {
				node.ReadyAt = time.Now()
			}
			return nil
		}
	}

	return fmt.Errorf("invalid transition: %s → %s for node %d", node.State, to, linodeID)
}

// RemoveNode removes a node from tracking. Should only be called after destruction.
func (nm *NodeManager) RemoveNode(linodeID int) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	delete(nm.nodes, linodeID)
}

// GetNode returns a node by Linode ID.
func (nm *NodeManager) GetNode(linodeID int) (*Node, bool) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	n, ok := nm.nodes[linodeID]
	return n, ok
}

// SetNodeIP updates the IPv4 address of a node.
func (nm *NodeManager) SetNodeIP(linodeID int, ip string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	node, exists := nm.nodes[linodeID]
	if !exists {
		return fmt.Errorf("node %d not found", linodeID)
	}
	node.IPv4 = ip
	return nil
}

// NodesByState returns all nodes in a given state.
func (nm *NodeManager) NodesByState(state NodeState) []*Node {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	var result []*Node
	for _, n := range nm.nodes {
		if n.State == state {
			result = append(result, n)
		}
	}
	return result
}

// ActiveCount returns the number of nodes not in DESTROYING state.
func (nm *NodeManager) ActiveCount() int {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	count := 0
	for _, n := range nm.nodes {
		if n.State != StateDestroying {
			count++
		}
	}
	return count
}

// ReadyCount returns the number of nodes in READY state.
func (nm *NodeManager) ReadyCount() int {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	count := 0
	for _, n := range nm.nodes {
		if n.State == StateReady {
			count++
		}
	}
	return count
}

// AllNodes returns a snapshot of all tracked nodes.
func (nm *NodeManager) AllNodes() []*Node {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	result := make([]*Node, 0, len(nm.nodes))
	for _, n := range nm.nodes {
		result = append(result, n)
	}
	return result
}

// FindNodeByIP returns a node by its IPv4 address.
func (nm *NodeManager) FindNodeByIP(ip string) (*Node, bool) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	for _, n := range nm.nodes {
		if n.IPv4 == ip {
			return n, true
		}
	}
	return nil, false
}

// IsScalingInProgress returns true if any node is in a transitional state.
func (nm *NodeManager) IsScalingInProgress() bool {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	for _, n := range nm.nodes {
		switch n.State {
		case StateProvisioning, StateDraining, StateDestroying:
			return true
		}
	}
	return false
}

// SetDrainLock sets or clears the drain lock on a node.
func (nm *NodeManager) SetDrainLock(linodeID int, locked bool) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	node, exists := nm.nodes[linodeID]
	if !exists {
		return fmt.Errorf("node %d not found", linodeID)
	}
	node.DrainLocked = locked
	return nil
}
