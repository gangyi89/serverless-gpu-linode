#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <env-file>"
  echo "Example: $0 deploy/envs/integration/control-plane.env"
  exit 1
fi

ENV_FILE="$1"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

docker compose \
  -f "${ROOT_DIR}/deploy/compose/control-plane.base.yml" \
  -f "${ROOT_DIR}/deploy/compose/control-plane.linode.yml" \
  --env-file "${ROOT_DIR}/${ENV_FILE}" \
  up -d

echo "Control plane deployed using ${ENV_FILE}"
