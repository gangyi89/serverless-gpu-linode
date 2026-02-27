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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("LOG_LEVEL")),
	})))

	cfg := loadConfig()
	slog.Info("starting nats-agent",
		"nats_url", cfg.NatsURL,
		"stream", cfg.StreamName,
		"subject", cfg.Subject,
		"consumer", cfg.ConsumerName,
		"queue_group", cfg.QueueGroup,
		"dlq_subject", cfg.DLQSubject,
		"ai_endpoint", cfg.AIEndpoint,
		"metrics_addr", cfg.MetricsAddr,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	agent := NewAgent(cfg)
	if err := agent.Run(ctx); err != nil {
		slog.Error("nats-agent failed", "error", err)
		os.Exit(1)
	}
	slog.Info("nats-agent stopped")
}

func loadConfig() Config {
	subject := envOrDefault("NATS_SUBJECT", envOrDefault("NATS_STREAM", "GPU_JOBS"))
	dlqSubject := envOrDefault("NATS_DLQ_SUBJECT", subject+".DLQ")

	return Config{
		NatsURL:      envOrDefault("NATS_URL", "nats://localhost:4222"),
		StreamName:   envOrDefault("NATS_STREAM", "GPU_JOBS"),
		Subject:      subject,
		ConsumerName: envOrDefault("NATS_CONSUMER", "gpu-workers"),
		QueueGroup:   envOrDefault("NATS_QUEUE_GROUP", "gpu-workers"),
		DLQSubject:   dlqSubject,
		DLQStream:    envOrDefault("NATS_DLQ_STREAM", "GPU_JOBS_DLQ"),

		AIEndpoint:   envOrDefault("AI_ENDPOINT", "http://ai-processor:8080/process"),
		HTTPTimeout:  envOrDefaultDuration("HTTP_TIMEOUT", 15*time.Minute),
		MaxInFlight:  envOrDefaultInt("MAX_INFLIGHT", 4),
		AckWait:      envOrDefaultDuration("ACK_WAIT", 20*time.Minute),
		MaxDeliver:   envOrDefaultInt("MAX_DELIVER", 5),
		RetryDelay:   envOrDefaultDuration("RETRY_DELAY", 5*time.Second),
		DrainTimeout: envOrDefaultDuration("DRAIN_TIMEOUT", 20*time.Second),
		MetricsAddr:  envOrDefault("METRICS_ADDR", ":9090"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

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
