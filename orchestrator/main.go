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
		"max_nodes", cfg.MaxNodes,
		"scale_up_threshold", cfg.ScaleUpThreshold,
		"scale_up_duration", cfg.ScaleUpDuration,
		"cooldown_duration", cfg.CooldownDuration,
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
	return Config{
		NatsURL:      envOrDefault("NATS_URL", "nats://localhost:4222"),
		StreamName:   envOrDefault("NATS_STREAM", "GPU_JOBS"),
		ConsumerName: envOrDefault("NATS_CONSUMER", "gpu-workers"),

		MaxNodes:         envOrDefaultInt("MAX_NODES", 2),
		ScaleUpThreshold: envOrDefaultInt("SCALE_UP_THRESHOLD", 10),
		ScaleUpDuration:  envOrDefaultDuration("SCALE_UP_DURATION", 5*time.Minute),
		CooldownDuration: envOrDefaultDuration("COOLDOWN_DURATION", 5*time.Minute),

		PrometheusTargetsFile: envOrDefault("PROMETHEUS_TARGETS_FILE", "/etc/prometheus/gpu_targets.json"),
		ListenAddr:            envOrDefault("LISTEN_ADDR", ":8081"),

		MonitorInterval: envOrDefaultDuration("MONITOR_INTERVAL", 10*time.Second),
	}
}

func loadLinodeClient() *LinodeClient {
	return NewLinodeClient(
		mustEnv("LINODE_TOKEN"),
		mustEnv("GOLDEN_IMAGE_ID"),
		envOrDefault("LINODE_REGION", "sg-sin-2"),
		envOrDefault("LINODE_TYPE", "g1-gpu-rtx6000-1"),
		os.Getenv("LINODE_ROOT_PASS"),
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
