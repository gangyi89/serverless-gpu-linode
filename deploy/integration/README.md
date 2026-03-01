# Integration Deployment

Use this folder for Linode or remote integration deployment.

## Files

- `control-plane.compose.yml`: control-plane services using prebuilt images.
- `gpu-node.compose.yml`: GPU node services using prebuilt images.
- `env/*.env.example`: templates for runtime env files.
- Orchestrator owns durable consumer settings (`NATS_SUBJECT`, `ACK_WAIT`, `MAX_DELIVER`) on control-plane.

## Quick Start

```bash
cp deploy/integration/env/control-plane.env.example deploy/integration/env/control-plane.env
cp deploy/integration/env/gpu-node.env.example deploy/integration/env/gpu-node.env

docker network create serverless-net || true

./scripts/deploy-control-plane.sh deploy/integration/env/control-plane.env
./scripts/deploy-gpu-node.sh deploy/integration/env/gpu-node.env
```

## Port Exposure Defaults

- `nats` and `orchestrator` are internal-only in integration (not published on host interfaces).
- `prometheus` (`9090`) is internal-only (reachable by containers on `serverless-net`).
- `grafana` (`3000`) is externally published for operator access.
- `GRAFANA_BIND_HOST` controls where Grafana binds (default `0.0.0.0` for external access).
