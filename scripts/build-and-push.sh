#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "Usage: $0 <registry> <tag>"
  echo "Example: $0 docker.io/your-dockerhub-user \$(git rev-parse --short HEAD)"
  exit 1
fi

REGISTRY="$1"
TAG="$2"
PREFIX="serverless-gpu-"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

build_and_push() {
  local name="$1"
  local context_dir="$2"
  local image="${REGISTRY}/${PREFIX}${name}:${TAG}"

  echo "Building ${image}"
  docker build -t "${image}" "${ROOT_DIR}/${context_dir}"

  echo "Pushing ${image}"
  docker push "${image}"
}

build_and_push "orchestrator" "orchestrator"
build_and_push "nats-agent" "nats-agent"
build_and_push "ai-processor" "ai-processor"

echo "Build and push complete."
echo "Export these for deploy:"
echo "  ORCHESTRATOR_IMAGE=${REGISTRY}/${PREFIX}orchestrator:${TAG}"
echo "  NATS_AGENT_IMAGE=${REGISTRY}/${PREFIX}nats-agent:${TAG}"
echo "  AI_PROCESSOR_IMAGE=${REGISTRY}/${PREFIX}ai-processor:${TAG}"
