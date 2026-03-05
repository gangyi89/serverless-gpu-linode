# Serverless GPU Auto-Scaling on Linode — Architecture Description

## 1. Overview

To build a serverless-like GPU environment that can scale from 0 to X GPU nodes on demand, paying only for compute time used. This document describes a lightweight, yet scalable solution to achieve this using an event-driven architecture with independent, single-responsibility services deployed as Docker containers.

---

## 2. High-Level Architecture

The system follows an event-driven pattern where GPU nodes consume work directly from a message queue. The Orchestrator is a pure infrastructure controller — it never touches request traffic. This separation ensures the data path (Gateway → NATS → GPU Node) has no unnecessary intermediaries.

```
┌──────────────┐       ┌────────────┐       ┌──────────────────┐
│              │       │            │◀──────│   GPU Node 1     │
│   Gateway    │──────▶│    NATS    │       │  (subscribes,    │
│   (Go)       │       │            │◀──────│   pulls work)    │
│              │       │            │       ├──────────────────┤
└──────────────┘       └─────┬──────┘       │   GPU Node 2     │
                             │              │  (subscribes,    │
                      monitor│queue depth   │   pulls work)    │
                             │              └────────┬─────────┘
                       ┌─────▼──────┐                │ metrics
                       │            │          ┌─────▼─────────────┐
                       │Orchestrator│◀─────────│   Prometheus +    │
                       │   (Go)     │ webhooks │   Alertmanager +  │
                       │            │──────────│   Grafana         │
                       │ Linode API │          └───────────────────┘
                       └────────────┘
```

---

## Current Implementation Notes

The current code implements the following behavior:

- **JetStream resources:** `GPU_JOBS` uses `WorkQueuePolicy`; `GPU_JOBS_DLQ` uses `LimitsPolicy`.
- **Agent consumption model:** pull-based with local slot control via `MAX_INFLIGHT` (bounded concurrent processing).
- **Ack semantics:** ack-after-completion; a message is acked only after `ai-processor` returns a `2xx`.
- **Long-running jobs:** the agent sends `InProgress()` heartbeats to extend the ack window while processing.
- **Scale-up signal:** orchestrator scales from queue pending work (`pending + ack_pending`).
- **Scale-down signal:** orchestrator scales down on request-idle windows when pending work remains `0` for configured durations.
- **Queue observability:** orchestrator exports separate `pending`, `ack_pending`, and combined pending-work metrics.

---

## 3. Components and Responsibilities

Each component is a single Docker container with a single responsibility.

### 3.1 Gateway (Custom Go Container)

**Responsibility:** Accept inbound HTTP requests, validate them, publish to NATS, and return HTTP 202 Accepted.

The Gateway is the stable public endpoint that exists regardless of how many GPU nodes are running. It is deliberately kept thin — it does not know about Linode, node state, or scaling logic.

- Accept HTTP requests from clients
- Validate request payload and authentication
- Publish the job message to a NATS JetStream subject
- Return HTTP 202 with a job ID for the client to poll or receive results via callback
- Expose a `/health` endpoint for liveness checks

The Gateway never communicates with the Orchestrator. It publishes to NATS and nothing else.

### 3.2 NATS (Off-the-Shelf `nats:latest` Container)

**Responsibility:** Buffer and distribute messages between the Gateway and GPU Nodes.

NATS is a lightweight message queue (~20MB Docker image, single binary, written in Go). It serves as the decoupling layer between request ingestion and GPU processing. JetStream is enabled for message durability — if a GPU node crashes mid-processing, the unacknowledged message is redelivered.

- Receive published messages from the Gateway
- Distribute work to GPU Nodes via queue groups (round-robin load balancing with no custom logic required)
- Hold messages during cold starts until a GPU Node subscribes and begins consuming
- Provide JetStream durability for at-least-once delivery
- Expose monitoring endpoints for Prometheus scraping (pending message count, consumer lag)

**Why NATS:** RabbitMQ and Redis are overkill for a 0–2 node system. NATS is purpose-built for this use case, has an excellent Go client, and requires near-zero configuration.

### 3.3 GPU Nodes (0–X Ephemeral Linode GPU VMs)

**Responsibility:** Subscribe to NATS, pull and process GPU workloads, expose metrics.

GPU Nodes are not permanent infrastructure. They are created and destroyed by the Orchestrator on demand. Each node is provisioned from a **golden image snapshot** that contains the base VM dependencies: NVIDIA GPU drivers, CUDA toolkit, Docker, and NVIDIA Container Toolkit. All application logic runs as containers within the VM via Docker Compose.

#### 3.3.1 Internal Container Architecture

Each GPU VM runs a small compose stack centered on `nats-agent` and `ai-processor`. In integration deployments, the node also runs exporters for observability (`dcgm-exporter` and `node-exporter`).

```
┌──────────────────────────────────────────────────────┐
│                      GPU VM                          │
│  (Golden Image: drivers, Docker, NVIDIA Toolkit)     │
│                                                      │
│  ┌────────────────┐       ┌───────────────────────┐  │
│  │  NATS Client   │       │   AI Processor        │  │
│  │  Agent         │──────▶│   Container           │  │
│  │                │ HTTP  │                       │  │
│  │ - Pull consumer│       │ - GPU inference/      │  │
│  │   on JetStream │       │   training workload   │  │
│  │ - Forward to   │       │ - Exposes /process    │  │
│  │   AI endpoint  │       │ - Infra-agnostic      │  │
│  │ - Ack on 2xx   │       │   workload service    │  │
│  │ - Retry + DLQ  │       │                       │  │
│  └────────────────┘       └───────────────────────┘  │
│                                                      │
│  ┌────────────────┐       ┌───────────────────────┐  │
│  │ DCGM Exporter  │       │   Node Exporter       │  │
│  │ (GPU metrics)  │       │   (system metrics)    │  │
│  └────────────────┘       └───────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

**NATS Client Agent (Custom Go Container):** The agent bridges JetStream and the workload container. It binds to the durable consumer, fetches messages in pull mode, forwards each payload to `POST http://ai-processor:8080/process`, sends `InProgress()` heartbeats while processing, retries transient failures, and sends terminal failures to DLQ. It acks only after a successful `2xx` response from `ai-processor`.

**AI Processor Container (Custom Image):** The AI Processor is a pure workload service. It exposes an HTTP endpoint, accepts a job payload, and performs GPU computation (inference, training, etc.). It has no direct dependency on NATS or Linode APIs.

**DCGM Exporter (`nvidia/dcgm-exporter`):** Exports NVIDIA GPU metrics (utilization, memory usage, temperature) to Prometheus (integration stack).

**Node Exporter (`prom/node-exporter`):** Exports standard system metrics (CPU, memory, disk) to Prometheus.

#### 3.3.3 NATS Message Delivery

The agent binds to a durable JetStream consumer and fetches messages in pull mode. After delivery, a message enters **ack pending** state and is not visible for redelivery unless the ack window expires or the message is negatively acknowledged.

Current behavior is ack-after-processing:

- Forward payload to `POST /process` on `ai-processor`
- Keep the message lease alive with `InProgress()` heartbeats while processing
- Ack on successful `2xx` response
- Retry transient failures locally
- Publish terminal failures to DLQ and ack the original message

#### 3.3.4 Event-Driven Consumption

Each GPU node agent binds to the same durable consumer and requests work via pull fetch. Effective load distribution comes from independent workers fetching when they have free in-flight capacity (`MAX_INFLIGHT`), so faster or less-loaded nodes naturally consume more messages.

#### 3.3.5 Graceful Drain

On shutdown (for example SIGTERM), the agent initiates subscription drain, stops accepting new work, and waits for in-flight handlers to finish up to `DRAIN_TIMEOUT`. If shutdown starts while a message is being handled, the agent avoids taking new work and releases unfinished work back to JetStream via negative ack behavior.

Current orchestrator scale-down is infrastructure-led: nodes are destroyed after idle checks, without a dedicated per-node drain handshake endpoint.

#### 3.3.6 Boot Sequence

1. VM boots from golden image (drivers, Docker, NVIDIA Toolkit pre-installed)
2. Docker Compose starts node services (`nats-agent`, `ai-processor`, and exporters)
3. `ai-processor` starts listening on `:8080`
4. `nats-agent` connects to NATS and binds to the durable consumer
5. Agent begins pull-fetching jobs and forwarding them to `ai-processor`
6. Prometheus discovers node targets and starts scraping exporter endpoints

#### 3.3.7 Golden Image vs Container Images

The golden image (Linode snapshot) should contain stable, slow-changing host dependencies: OS baseline, NVIDIA drivers, CUDA runtime/toolkit, Docker, and NVIDIA Container Toolkit. Application behavior lives in container images (`nats-agent`, `ai-processor`, and observability exporters), which are versioned and deployed independently.

This separation keeps host rebuild frequency low while allowing rapid application updates via image rollout.

### 3.4 Orchestrator (Custom Go Container)

**Responsibility:** Own autoscaling decisions and managed node lifecycle.

The orchestrator is an infrastructure controller. It does not process user requests. It ensures JetStream resources exist, evaluates queue state, provisions/destroys GPU nodes through Linode, and keeps Prometheus target discovery in sync.

Core responsibilities:

- **JetStream ownership:** ensures jobs stream, DLQ stream, and durable consumer exist with expected config.
- **Queue-based scaling:** evaluates `pending_work = pending + ack_pending` on each monitor tick.
- **Node lifecycle:** creates, tracks, reconciles, and destroys Linode GPU nodes.
- **Prometheus file SD:** rewrites GPU target file after node changes.
- **Operational endpoints:** exposes `GET /metrics` and `GET /health`.

Scaling logic details and variable definitions are documented in Section 4.

### 3.5 Monitoring Stack: Prometheus + Alertmanager + Grafana (Docker Containers)

**Responsibility:** Collect metrics, evaluate scale-down alert rules, visualize system state, and fire webhooks to the Orchestrator for scale-down events.

**Prometheus** scrapes metrics from the Orchestrator (`/metrics`), GPU Nodes (DCGM Exporter + Node Exporter), and NATS. It evaluates alerting rules defined in Grafana.

**Alertmanager** handles deduplication, grouping, silencing, and routing of alerts. For scaling purposes, Alertmanager is only involved in **scale-down** events — it fires a webhook POST to the Orchestrator's `/alerts` endpoint when GPU idle thresholds are breached. Scale-up is handled entirely by the Orchestrator via NATS queue depth monitoring and does not depend on Alertmanager.

**Grafana** provides operational dashboards for GPU utilization, queue depth, node count, consumer lag, and request latency. Scale-down threshold tuning happens in the Grafana UI — no code redeployment is needed to adjust sensitivity.

---

## 4. Scaling Logic

Scaling is currently fully orchestrator-driven from JetStream queue state.

- **Signal source:** `pending_work = num_pending + num_ack_pending`
- **Scale-up owner:** Orchestrator
- **Scale-down owner:** Orchestrator
- **No Alertmanager dependency** for scaling decisions in the current implementation

### 4.1 Decision Flow

```text
every MONITOR_INTERVAL:
  reconcile managed nodes from Linode API
  read queue stats (pending + ack_pending)

  scale-up:
    if pending_work > 0 and active_nodes == 0:
      scale 0 -> 1
    else if pending_work > SCALE_UP_THRESHOLD
         and sustained for SCALE_UP_DURATION
         and ready_nodes == active_nodes
         and active_nodes < MAX_NODES:
      scale N -> N+1

  scale-down:
    if active_nodes > MIN_NODES and ready_nodes > 0 and pending_work == 0:
      idle timer runs
      if active_nodes > 1 and idle >= SCALE_DOWN_IDLE_DURATION:
        scale N -> N-1
      if active_nodes == 1 and idle >= SCALE_TO_ZERO_IDLE_DURATION:
        scale 1 -> 0

  all scale operations require cooldown elapsed (COOLDOWN_DURATION)
```

### 4.2 Scale-Up Paths

**0 -> 1 (cold start)**

- Trigger: `pending_work > 0` with `active_nodes == 0`
- Action: create one GPU node via Linode API
- Expected behavior: queued jobs remain buffered in JetStream until worker is ready

**N -> N+1 (horizontal scale-out)**

- Trigger: `pending_work > SCALE_UP_THRESHOLD` continuously for `SCALE_UP_DURATION`
- Guards: no scale operation in progress, cooldown elapsed, all active nodes already ready, and below `MAX_NODES`
- Action: create one additional GPU node

### 4.3 Scale-Down Paths

Scale-down uses request-idle windows (queue-derived), not GPU utilization alerts.

**N -> N-1 (when active nodes > 1)**

- Trigger: `pending_work == 0` for at least `SCALE_DOWN_IDLE_DURATION`
- Guards: cooldown elapsed, scale not already in progress, above `MIN_NODES`
- Target selection: highest Linode ID among ready nodes (newest-first heuristic)

**1 -> 0 (scale to zero)**

- Trigger: `pending_work == 0` for at least `SCALE_TO_ZERO_IDLE_DURATION`
- Guards: same as above
- Action: destroy the last ready node

### 4.4 Scaling Variables

Primary scaling env vars:

- `MIN_NODES` (default `0`)
- `MAX_NODES` (default `2`)
- `SCALE_UP_THRESHOLD` (default `10`)
- `SCALE_UP_DURATION` (default `5m`)
- `SCALE_DOWN_IDLE_DURATION` (default `5m`)
- `SCALE_TO_ZERO_IDLE_DURATION` (default `10m`)
- `COOLDOWN_DURATION` (default `5m`)
- `MONITOR_INTERVAL` (default `10s`)

Queue/consumer variables used by scaling:

- `NATS_STREAM`
- `NATS_SUBJECT`
- `NATS_CONSUMER`
- `ACK_WAIT`
- `MAX_DELIVER`

### 4.5 Safety Mechanisms

- **Cooldown gate:** each scale event is blocked until `COOLDOWN_DURATION` passes.
- **In-progress guard:** only one scale operation runs at a time.
- **Node floor/cap:** scaling never goes below `MIN_NODES` or above `MAX_NODES`.
- **Readiness guard for scale-out:** N->N+1 is allowed only when current active nodes are ready.
- **Idle timer reset:** any `pending_work > 0` resets scale-down idle tracking.

---

## 5. Infrastructure Layout

Control-plane services run continuously on a non-GPU host. GPU worker nodes are ephemeral and created/destroyed by the orchestrator.

```
Control Plane Stack:
├── api               (custom Go image)
├── nats              (nats:latest, JetStream enabled)
├── orchestrator      (custom Go image)
├── nats-exporter     (natsio/prometheus-nats-exporter)
├── prometheus        (prom/prometheus)
└── grafana           (grafana/grafana)

GPU Node Stack (ephemeral, 0..MAX_NODES):
└── Provisioned from Linode golden image
    ├── host baseline (OS + NVIDIA drivers + CUDA + Docker + NVIDIA toolkit)
    └── compose services
        ├── nats-agent        (custom Go image)
        ├── ai-processor      (custom image)
        ├── node-exporter     (prom/node-exporter)
        └── dcgm-exporter     (nvidia/dcgm-exporter, integration stack)
```

Deployment references:

- Local: `deploy/local/control-plane.compose.yml`, `deploy/local/gpu-node.compose.yml`
- Integration: `deploy/integration/control-plane.compose.yml`, `deploy/integration/gpu-node.compose.yml`

---

## 6. Data Flow Summary

**Happy path (nodes running):**
1. Client sends `POST /v1/jobs` to `api`
2. `api` validates payload and publishes to JetStream subject (`NATS_SUBJECT`)
3. `api` returns HTTP `202` with `jobId`
4. `nats-agent` fetches one message from the durable consumer
5. Agent forwards payload to `ai-processor` (`POST /process`)
6. While processing, agent sends `InProgress()` heartbeats
7. On success (`2xx`), agent acks the message
8. `ai-processor` returns completion response to the agent

**Cold start path (no nodes):**
1. Client submits job to `api`; job is persisted in JetStream
2. Orchestrator sees `pending_work > 0` with zero active nodes
3. Orchestrator provisions a GPU VM from the golden image
4. VM boots and starts `nats-agent` + `ai-processor`
5. Agent binds to the durable consumer and begins pull-fetching
6. Buffered job is processed and acknowledged

**Failure path (current behavior):**

1. If forwarding fails, agent retries locally (`MAX_DELIVER`, `RETRY_DELAY`)
2. If retries are exhausted or failure is non-retryable (`4xx`), agent publishes a DLQ envelope
3. Agent acks the original message after DLQ publish

---

## 7. Open Considerations

**Cold start latency.** 0->1 scale events include VM provisioning and container startup time. If this is too high for your SLO, consider warm capacity (`MIN_NODES=1`) or a pre-warmed standby strategy.

**Scale-down safety.** Current scale-down is queue-idle based and infra-led. If stronger guarantees are required, add explicit worker drain coordination before node termination.

**DLQ operations.** DLQ writing exists, but replay/triage workflow is still an operational concern (tooling, dashboards, and runbooks).

**Golden image lifecycle.** Host dependencies in the snapshot still need periodic rebuilds for driver and security updates.

**Control-plane resiliency.** A single control-plane host remains a failure domain; consider backup/HA strategy and external uptime monitoring.