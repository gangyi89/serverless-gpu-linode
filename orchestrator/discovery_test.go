package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteTargetsFile_Empty(t *testing.T) {
	nm := NewNodeManager(2)
	path := filepath.Join(t.TempDir(), "targets.json")

	if err := WriteTargetsFile(path, nm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	var targets []PrometheusTarget
	if err := json.Unmarshal(data, &targets); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(targets) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(targets))
	}
}

func TestWriteTargetsFile_OneReadyNode(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1")
	nm.SetNodeIP(100, "10.0.0.1")
	nm.TransitionNode(100, StateReady)

	path := filepath.Join(t.TempDir(), "targets.json")

	if err := WriteTargetsFile(path, nm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(path)
	var targets []PrometheusTarget
	json.Unmarshal(data, &targets)

	// One node → two targets (DCGM on :9400, Node Exporter on :9100)
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets for 1 node, got %d", len(targets))
	}

	ports := map[string]bool{}
	for _, tgt := range targets {
		if len(tgt.Targets) != 1 {
			t.Fatalf("expected 1 address per target entry, got %d", len(tgt.Targets))
		}
		ports[tgt.Targets[0]] = true
	}

	if !ports["10.0.0.1:9400"] {
		t.Fatal("missing DCGM exporter target 10.0.0.1:9400")
	}
	if !ports["10.0.0.1:9100"] {
		t.Fatal("missing Node Exporter target 10.0.0.1:9100")
	}
}

func TestWriteTargetsFile_SkipsProvisioningNodes(t *testing.T) {
	nm := NewNodeManager(2)
	nm.AddNode(100, "gpu-1") // still provisioning
	nm.SetNodeIP(100, "10.0.0.1")

	path := filepath.Join(t.TempDir(), "targets.json")
	WriteTargetsFile(path, nm)

	data, _ := os.ReadFile(path)
	var targets []PrometheusTarget
	json.Unmarshal(data, &targets)

	if len(targets) != 0 {
		t.Fatalf("provisioning nodes should not appear in targets, got %d", len(targets))
	}
}

func TestWriteTargetsFile_TwoNodes(t *testing.T) {
	nm := NewNodeManager(2)

	nm.AddNode(100, "gpu-1")
	nm.SetNodeIP(100, "10.0.0.1")
	nm.TransitionNode(100, StateReady)

	nm.AddNode(200, "gpu-2")
	nm.SetNodeIP(200, "10.0.0.2")
	nm.TransitionNode(200, StateReady)

	path := filepath.Join(t.TempDir(), "targets.json")
	WriteTargetsFile(path, nm)

	data, _ := os.ReadFile(path)
	var targets []PrometheusTarget
	json.Unmarshal(data, &targets)

	// Two nodes → four targets
	if len(targets) != 4 {
		t.Fatalf("expected 4 targets for 2 nodes, got %d", len(targets))
	}
}
