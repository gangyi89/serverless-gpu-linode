# Integration Deployment

Use this folder for Linode or remote integration deployment.

## Files

- `control-plane.compose.yml`: control-plane services using prebuilt images.
- `gpu-node.compose.yml`: GPU node services using prebuilt images.
- `env/*.env.example`: templates for runtime env files.

## Quick Start

```bash
cp deploy/integration/env/control-plane.env.example deploy/integration/env/control-plane.env
cp deploy/integration/env/gpu-node.env.example deploy/integration/env/gpu-node.env

./scripts/deploy-control-plane.sh deploy/integration/env/control-plane.env
./scripts/deploy-gpu-node.sh deploy/integration/env/gpu-node.env
```
