## Run integration locally
docker network create serverless-net

### 1) Start control-plane
```bash
docker compose -f deploy/control-plane/docker-compose.yml up --build
```

### 2) Check JetStream streams
```bash
curl -sS "http://localhost:8222/jsz?streams=true" | jq -r '.account_details[].stream_detail[].name'
```

### 3) Check if `GPU_JOBS` exists
```bash
curl -sS "http://localhost:8222/jsz?streams=true" \
| jq -e '.account_details[].stream_detail[] | select(.name=="GPU_JOBS")' >/dev/null \
&& echo "exists" || echo "missing"
```

### 4) Create stream once (if missing)
```bash
docker run --rm --network serverless-net natsio/nats-box \
  nats --server nats://nats:4222 stream add GPU_JOBS --subjects GPU_JOBS --storage file --retention work --defaults
```

### 5) Publish one test message
```bash
docker run --rm --network serverless-net natsio/nats-box \
  nats --server nats://nats:4222 pub GPU_JOBS '{"job_id":"test-1","prompt":"hello"}'
```

### 6) Check current queue depth
```bash
curl -sS "http://localhost:8222/jsz?streams=true" \
| jq '[.account_details[].stream_detail[] | select(.name=="GPU_JOBS") | .state.messages][0] // 0'
```

### 7) Check pending vs acks pending vs total (from orchestrator)
```bash
curl -sS "http://localhost:8081/metrics" | rg "orchestrator_queue_(pending|ack_pending|depth)"
```

> Note: port `8222` is monitoring-only (`/jsz`, `/varz`, etc). Use `nats` CLI to publish.

### get the stream status
docker run --rm --network serverless-net natsio/nats-box \             
  nats --server nats://nats:4222 stream info GPU_JOBS


### deploy control-plane
docker compose -f deploy/control-plane/docker-compose.yml up -d --build 
### deploy gpu-workers
docker compose -f deploy/gpu-node/docker-compose.yml up -d --build 