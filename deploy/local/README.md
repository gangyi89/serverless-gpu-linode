# Local Deployment

Use this folder for local development workflows.

## Files

- `control-plane.compose.yml`: local stack (builds orchestrator from source).
- `gpu-node.compose.yml`: local GPU node stack (builds nats-agent and ai-processor from source).
- `env/*.env.example`: templates for local env files.

## Quick Start

```bash
cp deploy/local/env/control-plane.env.example deploy/local/env/control-plane.env
cp deploy/local/env/gpu-node.env.example deploy/local/env/gpu-node.env

docker compose -p control-plane -f deploy/local/control-plane.compose.yml --env-file deploy/local/env/control-plane.env up -d --build
docker compose -p gpu-node -f deploy/local/gpu-node.compose.yml --env-file deploy/local/env/gpu-node.env up -d --build
```

## Port Exposure Defaults

- `nats`, `orchestrator`, and `prometheus` are internal-only (no host-published ports).
- `grafana` is externally published on `3000` for operator access.
- `GRAFANA_BIND_HOST` controls Grafana host binding (default `0.0.0.0`).
