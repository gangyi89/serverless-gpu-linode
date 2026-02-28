package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
)

// PrometheusTarget represents a Prometheus file_sd target entry.
type PrometheusTarget struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// WriteTargetsFile writes the Prometheus file_sd targets JSON for all READY GPU nodes.
// Prometheus watches this file and automatically discovers new scrape targets.
func WriteTargetsFile(path string, nm *NodeManager) error {
	nodes := nm.NodesByState(StateReady)

	var targets []PrometheusTarget

	for _, node := range nodes {
		if node.IPv4 == "" {
			continue
		}

		// DCGM Exporter — GPU metrics on port 9400
		targets = append(targets, PrometheusTarget{
			Targets: []string{fmt.Sprintf("%s:9400", node.IPv4)},
			Labels: map[string]string{
				"job":       "dcgm-exporter",
				"linode_id": fmt.Sprintf("%d", node.LinodeID),
				"node":      node.Label,
			},
		})

		// Node Exporter — system metrics on port 9100
		targets = append(targets, PrometheusTarget{
			Targets: []string{fmt.Sprintf("%s:9100", node.IPv4)},
			Labels: map[string]string{
				"job":       "node-exporter",
				"linode_id": fmt.Sprintf("%d", node.LinodeID),
				"node":      node.Label,
			},
		})

		// NATS Agent — worker/queue bridge metrics on host port 19090
		targets = append(targets, PrometheusTarget{
			Targets: []string{fmt.Sprintf("%s:19090", node.IPv4)},
			Labels: map[string]string{
				"job":       "nats-agent",
				"linode_id": fmt.Sprintf("%d", node.LinodeID),
				"node":      node.Label,
			},
		})
	}

	// Write empty array instead of null when no targets
	if targets == nil {
		targets = []PrometheusTarget{}
	}

	data, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal targets: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write targets file: %w", err)
	}

	slog.Info("wrote prometheus targets file", "path", path, "target_count", len(targets))
	return nil
}
