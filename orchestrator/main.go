package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	// Configure structured JSON logging
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("LOG_LEVEL")),
	})))

	cfg := loadConfig()

	slog.Info("starting orchestrator",
		"nats_url", cfg.NatsURL,
		"stream", cfg.StreamName,
		"consumer", cfg.ConsumerName,
		"dlq_stream", cfg.DLQStream,
		"dlq_subject", cfg.DLQSubject,
		"linode_managed_tag", cfg.LinodeManagedTag,
		"linode_cluster_tag", cfg.LinodeClusterTag,
		"min_nodes", cfg.MinNodes,
		"max_nodes", cfg.MaxNodes,
		"scale_up_threshold", cfg.ScaleUpThreshold,
		"scale_up_duration", cfg.ScaleUpDuration,
		"cooldown_duration", cfg.CooldownDuration,
		"scale_down_idle_duration", cfg.ScaleDownIdleDuration,
		"scale_to_zero_idle_duration", cfg.ScaleToZeroIdleDuration,
		"listen_addr", cfg.ListenAddr,
	)

	// Graceful shutdown on SIGINT/SIGTERM
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cloud := loadLinodeClient()

	orch := NewOrchestrator(cfg, cloud)
	if err := orch.Run(ctx); err != nil {
		slog.Error("orchestrator failed", "error", err)
		os.Exit(1)
	}

	slog.Info("orchestrator stopped")
}

// loadConfig reads orchestrator configuration from environment variables with sensible defaults.
func loadConfig() Config {
	cfg := Config{
		NatsURL:      envOrDefault("NATS_URL", "nats://localhost:4222"),
		StreamName:   envOrDefault("NATS_STREAM", "GPU_JOBS"),
		ConsumerName: envOrDefault("NATS_CONSUMER", "gpu-workers"),
		DLQSubject:   envOrDefault("NATS_DLQ_SUBJECT", "GPU_JOBS.DLQ"),
		DLQStream:    envOrDefault("NATS_DLQ_STREAM", "GPU_JOBS_DLQ"),
		LinodeManagedTag: envOrDefault("LINODE_MANAGED_TAG", "serverless-gpu-managed"),
		LinodeClusterTag: envOrDefault("LINODE_CLUSTER_TAG", "serverless-gpu-default"),

		MinNodes:         envOrDefaultInt("MIN_NODES", 0),
		MaxNodes:         envOrDefaultInt("MAX_NODES", 2),
		ScaleUpThreshold: envOrDefaultInt("SCALE_UP_THRESHOLD", 10),
		ScaleUpDuration:  envOrDefaultDuration("SCALE_UP_DURATION", 5*time.Minute),
		CooldownDuration: envOrDefaultDuration("COOLDOWN_DURATION", 5*time.Minute),
		ScaleDownIdleDuration:   envOrDefaultDuration("SCALE_DOWN_IDLE_DURATION", 5*time.Minute),
		ScaleToZeroIdleDuration: envOrDefaultDuration("SCALE_TO_ZERO_IDLE_DURATION", 10*time.Minute),

		PrometheusTargetsFile: envOrDefault("PROMETHEUS_TARGETS_FILE", "/etc/prometheus/gpu_targets.json"),
		ListenAddr:            envOrDefault("LISTEN_ADDR", ":8081"),

		MonitorInterval: envOrDefaultDuration("MONITOR_INTERVAL", 10*time.Second),
	}

	if cfg.MinNodes < 0 {
		slog.Error("invalid min nodes", "min_nodes", cfg.MinNodes)
		os.Exit(1)
	}
	if cfg.MaxNodes < 1 {
		slog.Error("invalid max nodes", "max_nodes", cfg.MaxNodes)
		os.Exit(1)
	}
	if cfg.MinNodes > cfg.MaxNodes {
		slog.Error("invalid node floor/cap configuration", "min_nodes", cfg.MinNodes, "max_nodes", cfg.MaxNodes)
		os.Exit(1)
	}

	return cfg
}

func loadLinodeClient() *LinodeClient {
	return NewLinodeClient(
		mustEnv("LINODE_TOKEN"),
		mustEnv("GOLDEN_IMAGE_ID"),
		envOrDefault("LINODE_REGION", "sg-sin-2"),
		envOrDefault("LINODE_TYPE", "g1-gpu-rtx6000-1"),
		os.Getenv("LINODE_ROOT_PASS"),
		[]string{
			envOrDefault("LINODE_MANAGED_TAG", "serverless-gpu-managed"),
			envOrDefault("LINODE_CLUSTER_TAG", "serverless-gpu-default"),
		},
	)
}

// mustEnv reads a required environment variable or exits.
func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

// envOrDefault reads an environment variable with a fallback default.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envOrDefaultInt reads an integer environment variable with a fallback default.
func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			slog.Error("invalid integer env var", "key", key, "value", v)
			os.Exit(1)
		}
		return n
	}
	return fallback
}

// envOrDefaultDuration reads a duration environment variable with a fallback default.
// Accepts Go duration strings like "5m", "30s", "2m30s".
func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			slog.Error("invalid duration env var", "key", key, "value", v)
			os.Exit(1)
		}
		return d
	}
	return fallback
}

// parseLogLevel converts a string log level to slog.Level.
func parseLogLevel(level string) slog.Level {
	switch level {
	case "DEBUG", "debug":
		return slog.LevelDebug
	case "WARN", "warn":
		return slog.LevelWarn
	case "ERROR", "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
