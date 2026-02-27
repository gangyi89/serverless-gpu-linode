package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Config struct {
	NatsURL      string
	StreamName   string
	Subject      string
	ConsumerName string
	QueueGroup   string
	DLQSubject   string
	DLQStream    string

	AIEndpoint   string
	HTTPTimeout  time.Duration
	MaxInFlight  int
	AckWait      time.Duration
	MaxDeliver   int
	RetryDelay   time.Duration
	DrainTimeout time.Duration
	MetricsAddr  string
}

type Agent struct {
	cfg Config

	nc  *nats.Conn
	js  nats.JetStreamContext
	sub *nats.Subscription

	httpClient *http.Client
	metrics    *Metrics

	wg       sync.WaitGroup
	inflight chan struct{}

	shuttingDown atomic.Bool
}

type dlqEnvelope struct {
	OriginalSubject string `json:"original_subject"`
	OriginalDataB64 string `json:"original_data_b64"`
	Reason          string `json:"reason"`
	NumDelivered    uint64 `json:"num_delivered"`
	FailedAt        string `json:"failed_at"`
}

func NewAgent(cfg Config) *Agent {
	maxInFlight := cfg.MaxInFlight
	if maxInFlight <= 0 {
		maxInFlight = 1
	}

	return &Agent{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.HTTPTimeout,
		},
		metrics:  NewMetrics(),
		inflight: make(chan struct{}, maxInFlight),
	}
}

func (a *Agent) Run(ctx context.Context) error {
	if err := a.connectNATS(); err != nil {
		return fmt.Errorf("connect nats: %w", err)
	}
	defer a.nc.Close()

	if err := a.ensureDLQStream(); err != nil {
		return fmt.Errorf("ensure dlq stream: %w", err)
	}

	if err := a.startHTTPServer(ctx); err != nil {
		return err
	}

	if err := a.subscribe(); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer a.sub.Unsubscribe()

	consumeErrCh := make(chan error, 1)
	go func() {
		consumeErrCh <- a.consumeLoop(ctx)
	}()

	select {
	case <-ctx.Done():
		a.shuttingDown.Store(true)
		slog.Info("shutdown signal received, draining subscription")
		if err := a.sub.Drain(); err != nil {
			slog.Warn("failed to drain subscription", "error", err)
		}
	case err := <-consumeErrCh:
		if err != nil {
			return fmt.Errorf("consume loop failed: %w", err)
		}
	}

	return a.waitForInflight()
}

func (a *Agent) connectNATS() error {
	nc, err := nats.Connect(
		a.cfg.NatsURL,
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
		return err
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return err
	}

	a.nc = nc
	a.js = js
	return nil
}

func (a *Agent) ensureDLQStream() error {
	if _, err := a.js.StreamInfo(a.cfg.DLQStream); err == nil {
		return nil
	}

	_, err := a.js.AddStream(&nats.StreamConfig{
		Name:      a.cfg.DLQStream,
		Subjects:  []string{a.cfg.DLQSubject},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
	})
	if err == nil {
		slog.Info("created dlq stream", "stream", a.cfg.DLQStream, "subject", a.cfg.DLQSubject)
		return nil
	}

	if _, infoErr := a.js.StreamInfo(a.cfg.DLQStream); infoErr == nil {
		return nil
	}
	return err
}

func (a *Agent) subscribe() error {
	sub, err := a.js.PullSubscribe(
		a.cfg.Subject,
		a.cfg.ConsumerName,
		nats.BindStream(a.cfg.StreamName),
		nats.ManualAck(),
		nats.AckWait(a.cfg.AckWait),
		nats.MaxDeliver(a.cfg.MaxDeliver),
	)
	if err != nil {
		return err
	}
	a.sub = sub
	slog.Info("subscribed to jobs",
		"stream", a.cfg.StreamName,
		"subject", a.cfg.Subject,
		"consumer", a.cfg.ConsumerName,
		"mode", "pull",
		"max_inflight", cap(a.inflight),
	)
	return nil
}

func (a *Agent) consumeLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case a.inflight <- struct{}{}:
			a.metrics.InflightMessages.Inc()
		}

		msgs, err := a.sub.Fetch(1, nats.MaxWait(1*time.Second))
		if err != nil {
			<-a.inflight
			a.metrics.InflightMessages.Dec()
			if errors.Is(err, nats.ErrTimeout) {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if len(msgs) == 0 {
			<-a.inflight
			a.metrics.InflightMessages.Dec()
			continue
		}

		msg := msgs[0]
		a.metrics.MessagesReceived.Inc()
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			defer func() {
				<-a.inflight
				a.metrics.InflightMessages.Dec()
			}()
			a.processMessage(msg)
		}()
	}
}

func (a *Agent) processMessage(msg *nats.Msg) {
	if a.shuttingDown.Load() {
		slog.Debug("agent is shutting down, nacking message",
			"subject", msg.Subject,
			"size_bytes", len(msg.Data),
		)
		_ = a.nak(msg)
		a.metrics.MessagesNacked.Inc()
		return
	}

	metadata, _ := msg.Metadata()
	delivered := uint64(1)
	if metadata != nil {
		delivered = metadata.NumDelivered
	}
	slog.Debug("received message",
		"subject", msg.Subject,
		"size_bytes", len(msg.Data),
		"num_delivered", delivered,
	)

	if !json.Valid(msg.Data) {
		slog.Debug("message payload is invalid json, sending to dlq",
			"subject", msg.Subject,
			"num_delivered", delivered,
		)
		if err := a.publishToDLQ(msg, delivered, "invalid_json_payload"); err != nil {
			slog.Error("failed to publish invalid payload to dlq", "error", err)
			_ = a.nak(msg)
			a.metrics.MessagesNacked.Inc()
			return
		}
		a.metrics.MessagesDLQ.Inc()
		_ = msg.Ack()
		a.metrics.MessagesAcked.Inc()
		slog.Debug("invalid message moved to dlq and acked",
			"subject", msg.Subject,
			"num_delivered", delivered,
		)
		return
	}

	start := time.Now()
	stopHeartbeat := a.startAckProgressHeartbeat(msg, delivered)
	defer stopHeartbeat()

	slog.Debug("forwarding message to ai endpoint",
		"ai_endpoint", a.cfg.AIEndpoint,
		"subject", msg.Subject,
		"num_delivered", delivered,
	)
	respStatus, respBody, err := a.forwardToAI(msg.Data)
	latency := time.Since(start)
	a.metrics.ForwardLatency.Observe(latency.Seconds())
	slog.Debug("forward attempt completed",
		"subject", msg.Subject,
		"num_delivered", delivered,
		"http_status", respStatus,
		"latency_ms", latency.Milliseconds(),
		"error", err,
	)
	if err == nil && respStatus >= http.StatusOK && respStatus < http.StatusMultipleChoices {
		a.metrics.LastSuccessUnix.SetToCurrentTime()
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("failed to ack message", "error", ackErr)
			return
		}
		a.metrics.MessagesAcked.Inc()
		slog.Debug("message acked after successful forward",
			"subject", msg.Subject,
			"num_delivered", delivered,
			"http_status", respStatus,
		)
		return
	}

	a.metrics.ForwardFailures.Inc()
	a.metrics.LastFailureUnix.SetToCurrentTime()

	reason := buildFailureReason(respStatus, respBody, err)
	nonRetryable := isNonRetryableStatus(respStatus) || isRetryExhausted(delivered, a.cfg.MaxDeliver)
	if nonRetryable {
		slog.Debug("message considered non-retryable, sending to dlq",
			"subject", msg.Subject,
			"num_delivered", delivered,
			"reason", reason,
			"http_status", respStatus,
			"max_deliver", a.cfg.MaxDeliver,
		)
		if dlqErr := a.publishToDLQ(msg, delivered, reason); dlqErr != nil {
			slog.Error("failed to publish to dlq", "error", dlqErr)
			_ = a.nak(msg)
			a.metrics.MessagesNacked.Inc()
			return
		}
		a.metrics.MessagesDLQ.Inc()
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("failed to ack dlq'd message", "error", ackErr)
			return
		}
		a.metrics.MessagesAcked.Inc()
		slog.Debug("message moved to dlq and acked",
			"subject", msg.Subject,
			"num_delivered", delivered,
		)
		return
	}

	slog.Warn("forward failed, retrying",
		"reason", reason,
		"num_delivered", delivered,
	)
	if nakErr := a.nak(msg); nakErr != nil {
		slog.Error("failed to nak message", "error", nakErr)
		return
	}
	a.metrics.MessagesNacked.Inc()
	slog.Debug("message nacked for retry",
		"subject", msg.Subject,
		"num_delivered", delivered,
		"retry_delay", a.cfg.RetryDelay,
	)
}

func (a *Agent) forwardToAI(payload []byte) (status int, body string, err error) {
	req, err := http.NewRequest(http.MethodPost, a.cfg.AIEndpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return resp.StatusCode, strings.TrimSpace(string(bodyBytes)), nil
}

func (a *Agent) publishToDLQ(msg *nats.Msg, delivered uint64, reason string) error {
	env := dlqEnvelope{
		OriginalSubject: msg.Subject,
		OriginalDataB64: base64.StdEncoding.EncodeToString(msg.Data),
		Reason:          reason,
		NumDelivered:    delivered,
		FailedAt:        time.Now().UTC().Format(time.RFC3339),
	}

	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	slog.Debug("publishing message to dlq",
		"dlq_subject", a.cfg.DLQSubject,
		"original_subject", msg.Subject,
		"num_delivered", delivered,
		"reason", reason,
	)
	_, err = a.js.Publish(a.cfg.DLQSubject, payload)
	return err
}

func (a *Agent) nak(msg *nats.Msg) error {
	if a.cfg.RetryDelay > 0 {
		if err := msg.NakWithDelay(a.cfg.RetryDelay); err == nil {
			return nil
		}
	}
	return msg.Nak()
}

func (a *Agent) startAckProgressHeartbeat(msg *nats.Msg, delivered uint64) func() {
	interval := a.cfg.AckWait / 3
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if interval < 1*time.Second {
		interval = 1 * time.Second
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := msg.InProgress(); err != nil {
					slog.Warn("failed to extend ack progress window",
						"subject", msg.Subject,
						"num_delivered", delivered,
						"error", err,
					)
					continue
				}
				slog.Debug("extended ack progress window",
					"subject", msg.Subject,
					"num_delivered", delivered,
					"interval", interval,
				)
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}

func (a *Agent) startHTTPServer(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(a.metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if a.nc == nil || !a.nc.IsConnected() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("nats-disconnected"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:    a.cfg.MetricsAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	go func() {
		slog.Info("starting metrics server", "addr", a.cfg.MetricsAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server failed", "error", err)
		}
	}()
	return nil
}

func (a *Agent) waitForInflight() error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.wg.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(a.cfg.DrainTimeout):
		return fmt.Errorf("drain timeout exceeded (%s)", a.cfg.DrainTimeout)
	}
}

func isNonRetryableStatus(status int) bool {
	return status >= 400 && status < 500
}

func isRetryExhausted(numDelivered uint64, maxDeliver int) bool {
	if maxDeliver <= 0 {
		return false
	}
	return int(numDelivered) >= maxDeliver
}

func buildFailureReason(status int, body string, err error) string {
	if err != nil {
		return err.Error()
	}
	if body == "" {
		return fmt.Sprintf("http_status_%d", status)
	}
	return fmt.Sprintf("http_status_%d: %s", status, body)
}
