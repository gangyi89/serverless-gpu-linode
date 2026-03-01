SHELL := /bin/bash

REGISTRY ?= docker.io/your-dockerhub-user
TAG ?= latest

LOCAL_CONTROL_PLANE_ENV ?= deploy/local/env/control-plane.env
LOCAL_GPU_NODE_ENV ?= deploy/local/env/gpu-node.env
INTEGRATION_CONTROL_PLANE_ENV ?= deploy/integration/env/control-plane.env
INTEGRATION_GPU_NODE_ENV ?= deploy/integration/env/gpu-node.env

.PHONY: help builder-init build-push \
	local-network local-up-control-plane local-up-gpu-node local-up \
	local-down-control-plane local-down-gpu-node local-down \
	deploy-control-plane deploy-gpu-node

help:
	@echo "Available targets:"
	@echo ""
	@echo "  [Helpers]"
	@echo "  make builder-init                      # Init buildx multiarch builder"
	@echo "  make build-push REGISTRY=... TAG=...   # Build/push all service images"
	@echo "  make local-network                      # Create shared docker network"
	@echo ""
	@echo "  [Local Deployment]"
	@echo "  make local-up                           # Start local control-plane + gpu-node"
	@echo "  make local-down                         # Stop local control-plane + gpu-node"
	@echo ""
	@echo "  [Integration/Production Deployment]"
	@echo "  make deploy-control-plane               # Deploy control-plane stack"
	@echo "  make deploy-gpu-node                    # Deploy gpu-node stack"
	@echo ""
	@echo "Overridable variables:"
	@echo "  REGISTRY=$(REGISTRY)"
	@echo "  TAG=$(TAG)"
	@echo "  LOCAL_CONTROL_PLANE_ENV=$(LOCAL_CONTROL_PLANE_ENV)"
	@echo "  LOCAL_GPU_NODE_ENV=$(LOCAL_GPU_NODE_ENV)"
	@echo "  INTEGRATION_CONTROL_PLANE_ENV=$(INTEGRATION_CONTROL_PLANE_ENV)"
	@echo "  INTEGRATION_GPU_NODE_ENV=$(INTEGRATION_GPU_NODE_ENV)"

builder-init:
	docker buildx create --use --name multiarch-builder || docker buildx use multiarch-builder
	docker buildx inspect --bootstrap

build-push:
	./scripts/build-and-push.sh $(REGISTRY) $(TAG)

# ------------------------------------------------------------------------------
# Helpers
# ------------------------------------------------------------------------------
local-network:
	docker network create serverless-net || true

# ------------------------------------------------------------------------------
# Local deployment
# ------------------------------------------------------------------------------
local-up-control-plane: local-network
	docker compose -f deploy/local/control-plane.compose.yml --env-file $(LOCAL_CONTROL_PLANE_ENV) up -d --build

local-up-gpu-node: local-network
	docker compose -f deploy/local/gpu-node.compose.yml --env-file $(LOCAL_GPU_NODE_ENV) up -d --build

local-up: local-up-control-plane local-up-gpu-node

local-down-control-plane:
	docker compose -f deploy/local/control-plane.compose.yml --env-file $(LOCAL_CONTROL_PLANE_ENV) down

local-down-gpu-node:
	docker compose -f deploy/local/gpu-node.compose.yml --env-file $(LOCAL_GPU_NODE_ENV) down

local-down: local-down-gpu-node local-down-control-plane

# ------------------------------------------------------------------------------
# Integration/Production deployment
# ------------------------------------------------------------------------------
deploy-control-plane:
	./scripts/deploy-control-plane.sh $(INTEGRATION_CONTROL_PLANE_ENV)

deploy-gpu-node:
	./scripts/deploy-gpu-node.sh $(INTEGRATION_GPU_NODE_ENV)
