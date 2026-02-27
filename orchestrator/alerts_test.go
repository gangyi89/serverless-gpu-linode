package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newAlertTestOrchestrator() *Orchestrator {
	cloud := newFakeCloud()
	cfg := Config{
		MaxNodes:              2,
		CooldownDuration:      0,
		PrometheusTargetsFile: "/tmp/test_alerts_targets.json",
	}
	return NewOrchestrator(cfg, cloud)
}

func TestHandleAlerts_MethodNotAllowed(t *testing.T) {
	orch := newAlertTestOrchestrator()

	req := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	rr := httptest.NewRecorder()
	orch.HandleAlerts(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestHandleAlerts_BadJSON(t *testing.T) {
	orch := newAlertTestOrchestrator()

	req := httptest.NewRequest(http.MethodPost, "/alerts", bytes.NewBufferString("not json"))
	rr := httptest.NewRecorder()
	orch.HandleAlerts(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestHandleAlerts_GPUIdleByLinodeID(t *testing.T) {
	orch := newAlertTestOrchestrator()

	// Add a ready node
	orch.nodes.AddNode(100, "gpu-1")
	orch.nodes.SetNodeIP(100, "10.0.0.1")
	orch.nodes.TransitionNode(100, StateReady)

	payload := AlertmanagerPayload{
		Status: "firing",
		Alerts: []Alert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": "GPUIdle",
					"linode_id": "100",
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/alerts", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	orch.HandleAlerts(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Scale-down runs in a goroutine
	time.Sleep(100 * time.Millisecond)

	if orch.nodes.ActiveCount() != 0 {
		t.Fatalf("expected node to be destroyed, got %d active", orch.nodes.ActiveCount())
	}
}

func TestHandleAlerts_ResolveByIP(t *testing.T) {
	orch := newAlertTestOrchestrator()

	orch.nodes.AddNode(200, "gpu-2")
	orch.nodes.SetNodeIP(200, "10.0.0.2")
	orch.nodes.TransitionNode(200, StateReady)

	payload := AlertmanagerPayload{
		Status: "firing",
		Alerts: []Alert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": "GPUIdle",
					"instance":  "10.0.0.2:9400",
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/alerts", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	orch.HandleAlerts(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	time.Sleep(100 * time.Millisecond)

	if orch.nodes.ActiveCount() != 0 {
		t.Fatalf("expected node to be destroyed, got %d active", orch.nodes.ActiveCount())
	}
}

func TestHandleAlerts_IgnoresResolvedAlerts(t *testing.T) {
	orch := newAlertTestOrchestrator()

	orch.nodes.AddNode(100, "gpu-1")
	orch.nodes.SetNodeIP(100, "10.0.0.1")
	orch.nodes.TransitionNode(100, StateReady)

	payload := AlertmanagerPayload{
		Status: "resolved",
		Alerts: []Alert{
			{
				Status: "resolved",
				Labels: map[string]string{
					"alertname": "GPUIdle",
					"linode_id": "100",
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/alerts", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	orch.HandleAlerts(rr, req)

	time.Sleep(50 * time.Millisecond)

	if orch.nodes.ActiveCount() != 1 {
		t.Fatal("resolved alerts should not trigger scale-down")
	}
}

func TestHandleAlerts_IgnoresNonScalingAlerts(t *testing.T) {
	orch := newAlertTestOrchestrator()

	orch.nodes.AddNode(100, "gpu-1")
	orch.nodes.TransitionNode(100, StateReady)

	payload := AlertmanagerPayload{
		Status: "firing",
		Alerts: []Alert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": "HighCPU",
					"linode_id": "100",
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/alerts", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	orch.HandleAlerts(rr, req)

	time.Sleep(50 * time.Millisecond)

	if orch.nodes.ActiveCount() != 1 {
		t.Fatal("non-scaling alerts should not trigger scale-down")
	}
}
