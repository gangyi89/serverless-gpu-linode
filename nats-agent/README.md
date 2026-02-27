# NATS Agent Design (v1)

## Purpose

`nats-agent` is a thin bridge between NATS JetStream and the local AI processor.

It is responsible for:

- Pulling jobs from NATS.
- Forwarding each job to `AI_ENDPOINT` over HTTP.
- Applying explicit ack/retry/DLQ behavior.
- Exposing health and metrics endpoints.
- Supporting graceful drain on shutdown/scale-down.

It is **not** responsible for orchestration, scaling decisions, or cloud lifecycle.

## Architecture Boundary

Data path for one job:

1. Receive message from JetStream.
2. Forward message body to `POST /process` on AI processor.
3. Decide `Ack`/`Nak`/DLQ action based on outcome.

The orchestrator controls node lifecycle separately.

## Processing Model (Simple)

The agent uses a slot-based pull loop:

1. If `inflight < MAX_INFLIGHT`, fetch exactly **1** message from NATS.
2. Send it to AI processor.
3. On completion, apply `Ack`/`Nak`/DLQ policy.
4. Release the slot.
5. Repeat.

Implications:

- `MAX_INFLIGHT=1` -> strict one-at-a-time per node.
- `MAX_INFLIGHT=N` -> at most `N` concurrent jobs per node.
- The agent does not prefetch extra jobs beyond available slots.

## Delivery Semantics

- Delivery guarantee: **at-least-once**.
- Ack strategy: **Option A (ack-after-completion)**:
  - The AI endpoint must hold the request open until work finishes.
  - Agent calls `Ack()` only after the completion HTTP response (2xx).
- Duplicate handling: processor should be idempotent (prefer `job_id` in payload).
- Long-running jobs: agent sends JetStream `InProgress()` heartbeats while waiting for AI completion to prevent premature redelivery.

## Failure Handling Policy (DLQ-first)

### Success

- AI returns `2xx` completion response -> `Ack()`.

### Transient failures (retry)

- Network error, timeout, connection refused, HTTP `5xx` -> `Nak()` for redelivery.

### Non-retryable failures (send to DLQ)

- Invalid payload format.
- AI returns HTTP `4xx` for request-level errors.
- Retries exhausted (`NumDelivered >= MAX_DELIVER`).

Action for non-retryable path:

1. Publish DLQ envelope to `NATS_DLQ_SUBJECT`.
2. `Ack()` original message (to prevent poison-message loops).

## DLQ Envelope

Recommended DLQ payload fields:

- `original_subject`
- `original_data` (raw payload)
- `reason` (error category/message)
- `num_delivered`
- `first_seen_at` (optional)
- `failed_at`
- `job_id` (if available)

Default DLQ subject convention:

- Main subject: `GPU_JOBS`
- DLQ subject: `GPU_JOBS.DLQ`

## JetStream Consumer Settings (recommended)

- Durable consumer name (stable identity).
- `AckExplicit` policy.
- `AckWait` tuned to expected job duration, not just connection latency.
- `MaxDeliver` bounded (for example, 5-10).
- Pull-based consumption preferred for explicit backpressure control.

### Design Decision: Option A

This agent uses **ack-after-completion** semantics.

- A message is considered done only when AI processing is done.
- If a job runs longer than `ACK_WAIT`, the agent periodically sends `InProgress()` to extend the ack window.
- `HTTP_TIMEOUT` must be set high enough to cover expected processing time.
- For strict "1 node = 1 concurrent job", set `MAX_INFLIGHT=1`.

## Runtime Configuration

Suggested environment variables:

- `NATS_URL`
- `NATS_STREAM`
- `NATS_SUBJECT`
- `NATS_CONSUMER`
- `NATS_QUEUE_GROUP`
- `NATS_DLQ_SUBJECT`
- `AI_ENDPOINT`
- `HTTP_TIMEOUT`
- `MAX_INFLIGHT`
- `ACK_WAIT`
- `MAX_DELIVER`
- `METRICS_ADDR`

## Health and Observability

Endpoints:

- `/health` for liveness/readiness.
- `/metrics` for Prometheus scraping.

Key metrics:

- `nats_agent_messages_received_total`
- `nats_agent_messages_acked_total`
- `nats_agent_messages_nacked_total`
- `nats_agent_messages_dlq_total`
- `nats_agent_forward_latency_seconds`
- `nats_agent_forward_failures_total`
- `nats_agent_inflight_messages`

## Graceful Drain

On `SIGTERM` (or explicit drain signal):

1. Stop fetching new messages.
2. Let in-flight forwards finish (bounded timeout).
3. Ack/Nak/DLQ as needed.
4. Exit cleanly.

This enables safe node drain before VM termination.

## Out of Scope for v1

- Exactly-once processing.
- Full replay tooling for DLQ.
- Advanced per-job routing and prioritization.
