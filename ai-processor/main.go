package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	listenAddr := envOrDefault("LISTEN_ADDR", ":8080")
	processDuration := envOrDefaultDuration("PROCESS_DURATION", 20*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/process", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		slog.Info("processing job", "sleep", processDuration.String())
		select {
		case <-r.Context().Done():
			slog.Warn("request canceled while processing")
			w.WriteHeader(http.StatusRequestTimeout)
			return
		case <-time.After(processDuration):
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "completed",
			"duration": processDuration.String(),
		})
	})

	slog.Info("starting dummy ai-processor", "addr", listenAddr, "process_duration", processDuration.String())
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		slog.Error("ai-processor exited", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			slog.Warn("invalid duration env var, using fallback", "key", key, "value", v, "fallback", fallback.String())
			return fallback
		}
		return d
	}
	return fallback
}
