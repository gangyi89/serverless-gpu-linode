# Build and Deploy Guide

This project uses a root `Makefile` as the main command entrypoint.

## 1) Prerequisites

- Docker + Docker Compose plugin (`docker compose`)
- GNU `make`
- Docker registry login if pushing images (`docker login`)
- For integration/prod deploys: reachable servers with Docker installed

## 2) Discover available commands

```bash
make help
```

## 3) Build and push images

Initialize buildx once on your machine:

```bash
make builder-init
```

Build and push all service images (`orchestrator`, `nats-agent`, `ai-processor`):

```bash
make build-push REGISTRY=docker.io/<your-user> TAG=$(git rev-parse --short HEAD)
```

Image reference semantics expected by this repo:

- General format: `<registry>/<namespace>/<image-name>:<tag>` or `docker.io/<namespace>/<image-name>:<tag>`
- This project enforces image names with `serverless-gpu-` prefix.
- `REGISTRY` should include host + namespace (for Docker Hub: `docker.io/<your-user>`).
- `TAG` should be immutable per release (recommended: git SHA).

Images are pushed with this exact naming pattern:

- `ORCHESTRATOR_IMAGE = <REGISTRY>/serverless-gpu-orchestrator:<TAG>`
- `NATS_AGENT_IMAGE = <REGISTRY>/serverless-gpu-nats-agent:<TAG>`
- `AI_PROCESSOR_IMAGE = <REGISTRY>/serverless-gpu-ai-processor:<TAG>`

## 4) Local deployment (single machine)

Create local env files (first time):

```bash
cp deploy/local/env/control-plane.env.example deploy/local/env/control-plane.env
cp deploy/local/env/gpu-node.env.example deploy/local/env/gpu-node.env
```

Start local stacks:

```bash
make local-up
```

Stop local stacks:

```bash
make local-down
```

Notes:

- `make local-up` auto-creates `serverless-net` if missing.
- Local compose files build from source.

## 5) Integration/production deployment (separate servers)

Use separate servers for control-plane and GPU node.

Create env files on the corresponding server(s):

```bash
cp deploy/integration/env/control-plane.env.example deploy/integration/env/control-plane.env
cp deploy/integration/env/gpu-node.env.example deploy/integration/env/gpu-node.env
```

Deploy control-plane server:

```bash
make deploy-control-plane INTEGRATION_CONTROL_PLANE_ENV=deploy/integration/env/control-plane.env
```

Deploy GPU-node server:

```bash
make deploy-gpu-node INTEGRATION_GPU_NODE_ENV=deploy/integration/env/gpu-node.env
```

Notes:

- Integration compose files use prebuilt images, not local `build` contexts.
- Ensure `NATS_URL` in `deploy/integration/env/gpu-node.env` points to your control-plane NATS endpoint.
- Set image vars in env files to the exact built image refs:
  - `deploy/integration/env/control-plane.env`: `ORCHESTRATOR_IMAGE=...`
  - `deploy/integration/env/gpu-node.env`: `NATS_AGENT_IMAGE=...`, `AI_PROCESSOR_IMAGE=...`

## 6) Recommended release flow

1. Commit code changes.
2. Build and push tagged images.
3. Update integration env files with new image tags.
4. Deploy control-plane server.
5. Deploy GPU-node server.