# Serverless GPU Auto-Scaling on Linode — Architecture Description

## 1. Problem Statement

Linode does not offer native auto-scaling for GPU VMs. The customer requires a serverless-like GPU environment that can scale from 0 to 2 GPU nodes on demand, paying only for compute time used. This document describes a custom-built solution to achieve this using an event-driven architecture with independent, single-responsibility services deployed as Docker containers.

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

The codebase currently implements the following behavior (this section is authoritative if older sections differ):

- **NATS stream model:** `GPU_JOBS` uses JetStream `WorkQueuePolicy` (queue semantics), and `GPU_JOBS_DLQ` uses `LimitsPolicy` (retention for failed jobs).
- **NATS agent consume model:** pull-based, slot-limited fetch. The agent fetches one message only when `inflight < MAX_INFLIGHT`.
- **Ack semantics:** Option A (ack-after-completion). The agent acks only after the AI processor returns a completion `2xx` response.
- **Long-running jobs:** the agent sends `InProgress()` heartbeats while waiting so messages are not redelivered when `ACK_WAIT` is exceeded.
- **Scale-up signal:** orchestrator scales up from queue **total** work (`queued + inflight`), not just queued-only.
- **Scale-down signal:** orchestrator scales down by **request-idle time** (queue total stays `0` for configured duration), not by GPU utilization alerts.
- **Queue observability:** orchestrator exposes separate metrics for queued and inflight jobs, plus total unprocessed work.

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

### 3.3 GPU Nodes (0–2 Ephemeral Linode GPU VMs)

**Responsibility:** Subscribe to NATS, pull and process GPU workloads, expose metrics.

GPU Nodes are not permanent infrastructure. They are created and destroyed by the Orchestrator on demand. Each node is provisioned from a **golden image snapshot** that contains the base VM dependencies: NVIDIA GPU drivers, CUDA toolkit, Docker, and NVIDIA Container Toolkit. All application logic runs as containers within the VM via Docker Compose.

#### 3.3.1 Internal Container Architecture

Each GPU VM runs four containers as a compose stack. In Phase 1, the model is **fire-and-forget** — once the NATS Agent delivers a message to the AI Processor, the work is considered done. No response or result delivery is required.

```
┌──────────────────────────────────────────────────────┐
│                      GPU VM                          │
│  (Golden Image: drivers, Docker, NVIDIA Toolkit)     │
│                                                      │
│  ┌────────────────┐       ┌───────────────────────┐  │
│  │  NATS Client   │       │   AI Processor        │  │
│  │  Agent         │──────▶│   Container           │  │
│  │                │ HTTP  │                       │  │
│  │ - Subscribe to │       │ - GPU inference/      │  │
│  │   queue group  │       │   training workload   │  │
│  │ - Forward to   │       │ - Exposes endpoint    │  │
│  │   AI Processor │       │   on localhost:8080   │  │
│  │ - Ack on send  │       │ - Knows nothing about │  │
│  │                │       │   NATS or infra       │  │
│  └────────────────┘       └───────────────────────┘  │
│                                                      │
│  ┌────────────────┐       ┌───────────────────────┐  │
│  │ DCGM Exporter  │       │   Node Exporter       │  │
│  │ (GPU metrics)  │       │   (system metrics)    │  │
│  └────────────────┘       └───────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

**NATS Client Agent (Custom Go Container):** The agent is a sidecar that bridges the NATS queue and the AI Processor. It subscribes to the NATS queue group, pulls a message, forwards the payload to the AI Processor via a local HTTP call (`POST http://ai-processor:8080/process`), and immediately acks the NATS message. In Phase 1, delivery to the AI Processor is the completion point — the agent does not wait for a processing result.

**AI Processor Container (Custom Image):** The AI Processor is a pure workload container. It exposes an HTTP endpoint, accepts a job payload, and performs GPU computation (inference, training, etc.). It has no knowledge of NATS, queues, Linode, or any infrastructure concern. This separation allows the data science team to build, test, and update the AI container independently — they only need to honor the API contract.

**DCGM Exporter (`nvidia/dcgm-exporter`):** Exports NVIDIA GPU metrics (utilization, memory usage, temperature) to Prometheus.

**Node Exporter (`prom/node-exporter`):** Exports standard system metrics (CPU, memory, disk) to Prometheus.

#### 3.3.2 Docker Compose on Each GPU VM

```yaml
services:
  nats-agent:
    build: ./nats-agent
    environment:
      - NATS_URL=nats://control-plane-ip:4222
      - AI_ENDPOINT=http://ai-processor:8080/process
    depends_on:
      - ai-processor

  ai-processor:
    build: ./ai-processor
    deploy:
      resources:
        reservations:
          devices:
            - capabilities: [gpu]
    ports:
      - "8080:8080"

  dcgm-exporter:
    image: nvidia/dcgm-exporter:latest

  node-exporter:
    image: prom/node-exporter:latest
```

#### 3.3.3 NATS Message Delivery (Phase 1 — Fire-and-Forget)

Once NATS delivers a message to a node's agent, that message enters an **in-flight** state and is invisible to all other consumers. There is no risk of duplicate processing across nodes. In Phase 1, the agent acks the message as soon as it successfully delivers the payload to the AI Processor's HTTP endpoint. This keeps the queue moving and the implementation simple.

Future phases can introduce ack-after-completion semantics with AckWait timeouts and retry logic if guaranteed processing is required.

#### 3.3.4 Event-Driven Consumption

Each GPU Node's agent subscribes to the same NATS queue group. NATS distributes messages round-robin across all active subscribers. This provides load balancing across nodes with no custom logic.

#### 3.3.5 Graceful Drain

To drain a node before termination, the agent unsubscribes from the NATS queue group. Since Phase 1 uses fire-and-forget, the agent has no in-flight work to wait on — once unsubscribed, the node can be destroyed immediately.

#### 3.3.6 Boot Sequence

1. VM boots from golden image (drivers, Docker, NVIDIA Toolkit pre-installed)
2. Docker Compose starts all four containers
3. AI Processor container initializes and begins listening on localhost:8080
4. NATS Client Agent connects to NATS and subscribes to the queue group
5. Agent begins pulling and forwarding messages to the AI Processor
6. Prometheus discovers the node and begins scraping DCGM + Node Exporter metrics

#### 3.3.7 Golden Image vs Container Images

The golden image (Linode snapshot) contains only stable, slow-changing components: the OS, NVIDIA drivers, CUDA toolkit, Docker, NVIDIA Container Toolkit, and the Docker Compose file. All application logic (the NATS Agent and AI Processor) lives in container images pulled at boot time or pre-cached in the snapshot. This means the AI model and agent code can be updated by pushing new container images without rebuilding the VM snapshot.

### 3.4 Orchestrator (Custom Go Container)

**Responsibility:** Monitor queue depth, manage GPU node lifecycle via the Linode API, and maintain the scaling state machine.

The Orchestrator is a pure infrastructure controller. It never touches request traffic. Its sole concern is ensuring the right number of GPU nodes exist for the current level of demand.

- **Monitor NATS queue depth for scale-up** — subscribe to NATS JetStream metadata (pending message count). If pending > 0 and nodes = 0, trigger provisioning immediately (0→1). If pending > N for M minutes and nodes = 1, trigger scale-out (1→2). All scale-up decisions are made directly by the Orchestrator with no Grafana dependency.
- **Receive Alertmanager webhooks for scale-down** — listen for GPU idle alerts (2→1 and 1→0) from Alertmanager and act on them. Scale-down is the only scaling path that depends on Grafana/Alertmanager.
- **Linode API integration** — create GPU VMs from the golden image snapshot, poll for provisioning completion, and destroy VMs on scale-down
- **Prometheus service discovery** — when a node is provisioned or destroyed, write an updated JSON targets file (e.g., `/etc/prometheus/gpu_targets.json`) so Prometheus automatically discovers new nodes and stops scraping destroyed ones. This is a natural extension of the Orchestrator's lifecycle management — it already knows when nodes come and go.
- **State machine** — track node lifecycle states to prevent race conditions (see Section 4.6)
- **Expose `/metrics`** — publish Prometheus metrics including current queue depth, node count, node states, provisioning latency, and cooldown status
- **Cooldown logic** — enforce minimum intervals between scale events to prevent flapping
- **Graceful drain coordination** — before destroying a node, signal it to unsubscribe from NATS, wait for in-flight jobs to complete, then issue the destroy call

### 3.5 Monitoring Stack: Prometheus + Alertmanager + Grafana (Docker Containers)

**Responsibility:** Collect metrics, evaluate scale-down alert rules, visualize system state, and fire webhooks to the Orchestrator for scale-down events.

**Prometheus** scrapes metrics from the Orchestrator (`/metrics`), GPU Nodes (DCGM Exporter + Node Exporter), and NATS. It evaluates alerting rules defined in Grafana.

**Alertmanager** handles deduplication, grouping, silencing, and routing of alerts. For scaling purposes, Alertmanager is only involved in **scale-down** events — it fires a webhook POST to the Orchestrator's `/alerts` endpoint when GPU idle thresholds are breached. Scale-up is handled entirely by the Orchestrator via NATS queue depth monitoring and does not depend on Alertmanager.

**Grafana** provides operational dashboards for GPU utilization, queue depth, node count, consumer lag, and request latency. Scale-down threshold tuning happens in the Grafana UI — no code redeployment is needed to adjust sensitivity.

---

## 4. Scaling Logic

Scale-up and scale-down use different signals and owners. Scale-up is driven by **queue depth**, owned entirely by the Orchestrator — it is predictive and does not require a running GPU to measure. Scale-down is driven by **GPU idle state**, owned by Grafana/Alertmanager — the customer requires scale-down to be based on actual GPU activity, which only the DCGM Exporter on a running node can report.

### 4.1 Scale 0→1 (Cold Start)

- **Trigger:** A request arrives when no GPU nodes exist
- **Signal:** NATS pending message count transitions from 0 to ≥1
- **Owner:** Orchestrator (direct monitoring, immediate decision)
- **Mechanism:** The Orchestrator monitors NATS JetStream metadata. When pending messages appear and no nodes are in READY or PROVISIONING state, it immediately calls the Linode API to create a GPU VM from the golden image. Messages remain buffered in NATS until the node boots, subscribes, and begins consuming.
- **Expected latency:** 2–5 minutes (VM provisioning + driver init + application start)

Grafana is not involved in this transition. There is no GPU to measure, and the decision must be immediate.

### 4.2 Scale 1→2 (Horizontal Scale-Out)

- **Trigger:** The existing node cannot keep up with demand
- **Signal:** Queue depth growing beyond threshold
- **Owner:** Orchestrator (direct monitoring)
- **Recommended threshold:** `pending_messages > N for M minutes` (tunable)
- **Mechanism:** The Orchestrator monitors NATS JetStream pending message count. When messages are accumulating faster than the single node can consume them, the Orchestrator checks its state machine (is a scale-up already in progress?) and, if not, provisions a second node via the Linode API.

**Why queue depth for scale-up:** Queue depth is predictive — it detects demand outpacing capacity before user experience degrades. GPU utilization is reactive and lagging. An empty queue means the node is keeping up; a growing queue means it is not. This is a simpler and faster signal than waiting for GPU utilization to sustain above a threshold.

### 4.3 Scale 2→1 (Scale-In)

- **Trigger:** One node is idle
- **Signal:** GPU utilization at or near zero for a sustained period
- **Owner:** Grafana/Alertmanager → Orchestrator
- **Recommended alert rule:** `gpu_utilization < 10% for 5 minutes` on one of the two nodes (via DCGM Exporter)
- **Mechanism:** Alertmanager fires a webhook to the Orchestrator's `/alerts` endpoint. The Orchestrator selects the idle node, signals it to unsubscribe from NATS, then destroys the VM via the Linode API.

**Why GPU idle for scale-down:** An empty queue does not mean the GPU is idle. In a fire-and-forget model, the AI Processor may still be running a long job after the message has been acked and the queue is empty. Only GPU utilization (reported by DCGM Exporter) reflects whether the hardware is actually doing work.

### 4.4 Scale 1→0 (Scale to Zero)

- **Trigger:** The last node has been idle for a sustained period
- **Signal:** GPU utilization at or near zero for a longer sustained period
- **Owner:** Grafana/Alertmanager → Orchestrator
- **Recommended alert rule:** `gpu_utilization < 5% for 10 minutes` (via DCGM Exporter)
- **Mechanism:** Same as 2→1, but with a longer evaluation window and a stricter threshold. Scaling to zero is the most expensive transition to reverse (full cold start), so the system should be conservative before terminating the last node.

### 4.5 Scaling Summary

| Transition | Signal | Owner | Latency Tolerance |
|---|---|---|---|
| 0→1 | Queue depth > 0 | Orchestrator (direct) | Cold start (2–5m) |
| 1→2 | Queue depth > N for M min | Orchestrator (direct) | Moderate (2–5m) |
| 2→1 | GPU idle < 10% for 5 min | Grafana → Orchestrator | Not time-sensitive |
| 1→0 | GPU idle < 5% for 10 min | Grafana → Orchestrator | Not time-sensitive |

**Scale-up** = queue depth (Orchestrator owns, no Grafana dependency)
**Scale-down** = GPU idle (Grafana/Alertmanager owns, webhook to Orchestrator)

### 4.6 Safety Mechanisms

**State machine prevents race conditions.** The Orchestrator tracks each node through a strict lifecycle:

```
IDLE (0 nodes) → SCALING_UP → PROVISIONING → READY → DRAINING → SCALING_DOWN → IDLE
```

Duplicate alerts or threshold breaches that arrive while a transition is already in progress are ignored.

**Cooldown period.** A minimum of 5 minutes is enforced between any two scale events to prevent flapping (rapid scale-up/down cycles).

**Scale-down lock.** The workload can signal "don't kill me" during a long-running job (e.g., a training batch). The Orchestrator respects this flag and delays the drain until the lock is released.

**Max node cap.** A hard limit of 2 nodes is enforced in the Orchestrator regardless of queue depth.

---

## 5. Infrastructure Layout

All control plane services run on a single small always-on Linode instance (non-GPU, shared/Nanode tier). This is the only persistent infrastructure cost. GPU nodes are fully ephemeral.

```
Always-on Linode (Shared / Nanode):
├── docker-compose.yml
│   ├── gateway           (custom Go image)
│   ├── nats              (nats:latest, JetStream enabled)
│   ├── orchestrator      (custom Go image)
│   ├── prometheus        (prom/prometheus)
│   ├── alertmanager      (prom/alertmanager)
│   └── grafana           (grafana/grafana)

Ephemeral Linode GPU VMs (0–2):
└── Provisioned from golden image snapshot
    ├── OS + NVIDIA drivers + CUDA (baked into snapshot)
    ├── Docker + NVIDIA Container Toolkit (baked into snapshot)
    └── docker-compose.yml
        ├── nats-agent        (custom Go image — queue bridge)
        ├── ai-processor      (custom image — GPU workload)
        ├── dcgm-exporter     (nvidia/dcgm-exporter — GPU metrics)
        └── node-exporter     (prom/node-exporter — system metrics)
```

---

## 6. Data Flow Summary

**Happy path (nodes running):**
1. Client sends request to Gateway
2. Gateway validates and publishes to NATS
3. Gateway returns HTTP 202
4. GPU Node's NATS Agent pulls message from queue group
5. Agent forwards payload to AI Processor container
6. Agent acks the NATS message
7. AI Processor executes the workload (fire-and-forget)

**Cold start path (no nodes):**
1. Client sends request to Gateway
2. Gateway publishes to NATS, returns HTTP 202
3. Orchestrator detects pending messages in NATS with no active nodes
4. Orchestrator provisions GPU VM from golden image via Linode API
5. VM boots, NATS Agent subscribes to queue group
6. Agent pulls buffered message, forwards to AI Processor
7. AI Processor executes the workload (fire-and-forget)

---

## 7. Open Considerations

**Cold start tolerance.** If 2–5 minute cold starts are unacceptable, evaluate keeping one stopped GPU instance as a warm standby. Check Linode's billing policy for stopped GPU VMs — some providers charge for reserved GPU even when stopped.

**Golden image maintenance.** The snapshot requires a versioning and rebuild pipeline. Driver updates, security patches, and application updates all require a new snapshot. Automate with Packer or equivalent tooling.

**Observability for the control plane.** The always-on Linode is a single point of failure. Consider adding basic alerting (e.g., Uptime Kuma or an external ping service) to detect if the control plane itself goes down.