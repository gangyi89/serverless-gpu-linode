# Project Folder Structure

```
serverless-gpu/
├── README.md
├── STRUCTURE.md
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
│   ├── statemachine.go
│   ├── statemachine_test.go
│   ├── scaler.go
│   ├── scaler_test.go
│   ├── linode.go
│   ├── alerts.go
│   ├── alerts_test.go
│   ├── discovery.go
│   ├── discovery_test.go
│   └── metrics.go
│
├── nats-agent/
│   ├── go.mod
│   ├── go.sum
│   ├── Dockerfile
│   ├── main.go
│   ├── agent.go
│   ├── drain.go
│   └── agent_test.go
│
├── ai-processor/
│   ├── go.mod
│   ├── go.sum
│   ├── Dockerfile
│   └── main.go                           # Fake HTTP server on :8080/process
│
├── deploy/
│   ├── control-plane/
│   │   ├── docker-compose.yml
│   │   ├── .env.example
│   │   ├── nats/
│   │   │   └── nats-server.conf
│   │   ├── prometheus/
│   │   │   ├── prometheus.yml
│   │   │   ├── alert_rules.yml
│   │   │   └── gpu_targets.json
│   │   ├── alertmanager/
│   │   │   └── alertmanager.yml
│   │   └── grafana/
│   │       └── provisioning/
│   │           ├── datasources/
│   │           │   └── prometheus.yml
│   │           └── dashboards/
│   │               ├── dashboard.yml
│   │               └── gpu-overview.json
│   └── gpu-node/
│       ├── docker-compose.yml
│       ├── .env.example
│       └── setup.sh
│
└── scripts/
    ├── build-all.sh
    ├── deploy-control-plane.sh
    ├── build-golden-image.sh
    └── integration-test.sh
```
