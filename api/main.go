package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
)

type Config struct {
	ListenAddr      string
	NatsURL         string
	NatsSubject     string
	RequestMaxBytes int64
}

type API struct {
	cfg Config
	nc  *nats.Conn
	js  nats.JetStreamContext
}

type createJobRequest struct {
	Prompt string `json:"prompt"`
	JobID  string `json:"jobId,omitempty"`
}

type jobMessage struct {
	JobID     string            `json:"job_id"`
	CreatedAt string            `json:"created_at"`
	Prompt    string            `json:"prompt"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type createJobResponse struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("LOG_LEVEL")),
	})))

	cfg := loadConfig()
	slog.Info("starting api service",
		"listen_addr", cfg.ListenAddr,
		"nats_url", cfg.NatsURL,
		"nats_subject", cfg.NatsSubject,
		"request_max_bytes", cfg.RequestMaxBytes,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	api, err := newAPI(cfg)
	if err != nil {
		slog.Error("failed to initialize api", "error", err)
		os.Exit(1)
	}
	defer api.nc.Close()

	if err := api.run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("api stopped with error", "error", err)
		os.Exit(1)
	}
	slog.Info("api stopped")
}

func newAPI(cfg Config) (*API, error) {
	nc, err := nats.Connect(
		cfg.NatsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("nats disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			slog.Info("nats reconnected")
		}),
	)
	if err != nil {
		return nil, err
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, err
	}

	return &API{
		cfg: cfg,
		nc:  nc,
		js:  js,
	}, nil
}

func (a *API) run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs", a.handleCreateJob)
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/openapi.json", a.handleOpenAPI)
	mux.HandleFunc("/docs", a.handleDocs)

	server := &http.Server{
		Addr:              a.cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	slog.Info("http server listening", "addr", a.cfg.ListenAddr)
	return server.ListenAndServe()
}

func (a *API) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer r.Body.Close()
	limited := http.MaxBytesReader(w, r.Body, a.cfg.RequestMaxBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()

	var req createJobRequest
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "prompt is required",
		})
		return
	}

	jobID := strings.TrimSpace(req.JobID)
	if jobID == "" {
		generatedID, err := newJobID()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create job id"})
			return
		}
		jobID = generatedID
	}

	msg := jobMessage{
		JobID:     jobID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Prompt:    req.Prompt,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to encode message"})
		return
	}

	ack, err := a.js.Publish(a.cfg.NatsSubject, payload)
	if err != nil {
		slog.Error("failed to publish job", "error", err, "subject", a.cfg.NatsSubject)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "queue unavailable"})
		return
	}

	slog.Info("job queued",
		"job_id", jobID,
		"subject", a.cfg.NatsSubject,
		"stream", ack.Stream,
		"sequence", ack.Sequence,
	)
	writeJSON(w, http.StatusAccepted, createJobResponse{
		JobID:  jobID,
		Status: "queued",
	})
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if a.nc == nil || !a.nc.IsConnected() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "nats-disconnected"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(openAPISpec))
}

func (a *API) handleDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerHTML))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func newJobID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func loadConfig() Config {
	return Config{
		ListenAddr:      envOrDefault("LISTEN_ADDR", ":8080"),
		NatsURL:         envOrDefault("NATS_URL", "nats://localhost:4222"),
		NatsSubject:     envOrDefault("NATS_SUBJECT", "GPU_JOBS"),
		RequestMaxBytes: envOrDefaultInt64("REQUEST_MAX_BYTES", 1<<20),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			slog.Error("invalid integer env var", "key", key, "value", v, "error", err)
			os.Exit(1)
		}
		return n
	}
	return fallback
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Serverless GPU API",
    "version": "1.0.0",
    "description": "HTTP API for submitting jobs into NATS."
  },
  "servers": [
    { "url": "/" }
  ],
  "paths": {
    "/health": {
      "get": {
        "summary": "Health check",
        "responses": {
          "200": {
            "description": "Service is healthy"
          },
          "503": {
            "description": "NATS is disconnected"
          }
        }
      }
    },
    "/v1/jobs": {
      "post": {
        "summary": "Queue a new job",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "$ref": "#/components/schemas/CreateJobRequest"
              }
            }
          }
        },
        "responses": {
          "202": {
            "description": "Job accepted",
            "content": {
              "application/json": {
                "schema": {
                  "$ref": "#/components/schemas/CreateJobResponse"
                }
              }
            }
          },
          "400": {
            "description": "Invalid request body"
          },
          "503": {
            "description": "NATS unavailable"
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "CreateJobRequest": {
        "type": "object",
        "required": ["prompt"],
        "properties": {
          "prompt": {
            "type": "string",
            "example": "hello world"
          },
          "jobId": {
            "type": "string",
            "example": "123"
          }
        }
      },
      "CreateJobResponse": {
        "type": "object",
        "properties": {
          "jobId": { "type": "string" },
          "status": { "type": "string", "example": "queued" }
        }
      }
    }
  }
}`

const swaggerHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Serverless GPU API Docs</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script>
      window.ui = SwaggerUIBundle({
        url: '/openapi.json',
        dom_id: '#swagger-ui',
        deepLinking: true
      });
    </script>
  </body>
</html>`
