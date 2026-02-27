package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// AlertmanagerPayload represents the webhook payload sent by Alertmanager.
type AlertmanagerPayload struct {
	Version  string  `json:"version"`
	GroupKey  string  `json:"groupKey"`
	Status   string  `json:"status"`
	Alerts   []Alert `json:"alerts"`
}

// Alert represents a single alert within an Alertmanager webhook payload.
type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
}

// HandleAlerts processes incoming Alertmanager webhooks for scale-down events.
// Scale-down is the only scaling path that depends on Alertmanager.
func (o *Orchestrator) HandleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload AlertmanagerPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		slog.Error("failed to decode alertmanager payload", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	slog.Info("received alertmanager webhook",
		"status", payload.Status,
		"alert_count", len(payload.Alerts),
	)

	for _, alert := range payload.Alerts {
		if alert.Status != "firing" {
			continue
		}

		alertName := alert.Labels["alertname"]
		if alertName != "GPUIdle" && alertName != "GPUIdleScaleToZero" {
			slog.Debug("ignoring non-scaling alert", "alertname", alertName)
			continue
		}

		linodeID, err := o.resolveNodeFromAlert(alert)
		if err != nil {
			slog.Warn("could not resolve node from alert", "error", err, "labels", alert.Labels)
			continue
		}

		slog.Info("scale-down alert received", "alertname", alertName, "linode_id", linodeID)

		go func(id int) {
			if err := o.ScaleDown(r.Context(), id); err != nil {
				slog.Error("scale-down failed", "linode_id", id, "error", err)
			}
		}(linodeID)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "accepted")
}

// resolveNodeFromAlert extracts the Linode ID from alert labels.
// Checks for a direct "linode_id" label first, then falls back to resolving by instance IP.
func (o *Orchestrator) resolveNodeFromAlert(alert Alert) (int, error) {
	// Direct linode_id label (set in Prometheus relabeling or alert rule)
	if idStr, ok := alert.Labels["linode_id"]; ok {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return 0, fmt.Errorf("invalid linode_id label: %s", idStr)
		}
		return id, nil
	}

	// Fallback: resolve by instance IP (e.g. "192.168.1.100:9400")
	instance := alert.Labels["instance"]
	if instance == "" {
		return 0, fmt.Errorf("alert has no linode_id or instance label")
	}

	ip := strings.Split(instance, ":")[0]
	node, found := o.nodes.FindNodeByIP(ip)
	if !found {
		return 0, fmt.Errorf("no tracked node with IP %s", ip)
	}

	return node.LinodeID, nil
}
