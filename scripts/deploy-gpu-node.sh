#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <env-file>"
  echo "Example: $0 deploy/envs/integration/gpu-node.env"
  exit 1
fi

ENV_FILE="$1"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

docker compose \
  -f "${ROOT_DIR}/deploy/compose/gpu-node.base.yml" \
  --env-file "${ROOT_DIR}/${ENV_FILE}" \
  up -d

echo "GPU node stack deployed using ${ENV_FILE}"
