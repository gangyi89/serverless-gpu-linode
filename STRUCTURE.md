# Project Folder Structure

```
serverless-gpu/
├── README.md
├── STRUCTURE.md
├── INTEGRATIONTEST.md
├── .gitignore
│
├── gateway/
│   ├── go.mod
│   ├── go.sum
│   ├── Dockerfile
│   ├── main.go
│   ├── server.go
│   ├── handler.go
│   └── handler_test.go
│
├── orchestrator/
│   ├── go.mod
│   ├── go.sum
│   ├── Dockerfile
│   ├── main.go
│   ├── orchestrator.go
│   ├── scaler.go
│   ├── statemachine.go
│   ├── linode.go
│   ├── discovery.go
│   ├── metrics.go
│   └── *_test.go
│
├── nats-agent/
│   ├── go.mod
│   ├── go.sum
│   ├── Dockerfile
│   ├── main.go
│   ├── agent.go
│   ├── metrics.go
│   └── *_test.go
│
├── ai-processor/
│   ├── go.mod
│   ├── go.sum
│   ├── Dockerfile
│   └── main.go
│
├── deploy/
│   ├── integration/                       # Integration deploy entrypoint
│   │   ├── control-plane.compose.yml
│   │   ├── gpu-node.compose.yml
│   │   └── env/
│   │       ├── control-plane.env.example
│   │       └── gpu-node.env.example
│   │
│   ├── local/                             # Local deploy entrypoint
│   │   ├── control-plane.compose.yml
│   │   ├── gpu-node.compose.yml
│   │   └── env/
│   │       ├── control-plane.env.example
│   │       └── gpu-node.env.example
│   │
│   └── shared/                            # Shared monitoring assets and file_sd
│       ├── alertmanager/
│       │   └── alertmanager.yml
│       ├── prometheus/
│       │   ├── prometheus.yml
│       │   ├── alert_rules.yml
│       │   └── file_sd/gpu_targets.json
│       └── grafana/
│           ├── dashboards/
│           │   ├── gpu-node-overview.json
│           │   ├── orchestrator-overview.json
│           │   ├── nats-jetstream.json
│           │   └── serverless-gpu.json
│           └── provisioning/
│               ├── datasources/prometheus.yml
│               └── dashboards/dashboard.yml
│
└── scripts/
    ├── build-and-push.sh
    ├── deploy-control-plane.sh
    └── deploy-gpu-node.sh
```

## Deployment Notes

- Use `deploy/integration/*.compose.yml` with env files in `deploy/integration/env/`.
- Use `deploy/local/*.compose.yml` with env files in `deploy/local/env/`.
- Shared observability configs live under `deploy/shared/`.
- Build/push immutable images first, then deploy with image tags pinned in env files.
