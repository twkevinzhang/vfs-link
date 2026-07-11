#!/usr/bin/env bash
# Deploy to self-hosted over SSH. The deployment core runs on the server so
# production secrets and local object storage stay on the host.
set -euo pipefail

DEPLOY_HOST="${DEPLOY_HOST:-self-hosted}"
DEPLOY_USER="${DEPLOY_USER:-}"
DEPLOY_PORT="${DEPLOY_PORT:-}"
DEPLOY_DIR="${DEPLOY_DIR:-~/vfs-link}"
DEPLOY_BRANCH="${DEPLOY_BRANCH:-main}"
DEPLOY_REMOTE="${DEPLOY_REMOTE:-}"
DEPLOY_SHA="${DEPLOY_SHA:-}"
IMAGE_NAME="${IMAGE_NAME:-vfs-link/ftp-server}"
DOCKERFILE="${DOCKERFILE:-apps/ftp-server/Dockerfile}"
SERVICE="${SERVICE:-ftp-server}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.self-hosted.yml}"
HEALTHCHECK_URL="${HEALTHCHECK_URL:-}"
VITE_BASE_PATH="${VITE_BASE_PATH:-/vfs-link/index}"
VITE_API_BASE_URL="${VITE_API_BASE_URL:-/vfs-link}"

SSH_TARGET="$DEPLOY_HOST"
if [ -n "$DEPLOY_USER" ]; then
  SSH_TARGET="$DEPLOY_USER@$DEPLOY_HOST"
fi

SSH_OPTS=(-o BatchMode=yes)
if [ -n "$DEPLOY_PORT" ]; then
  SSH_OPTS+=(-p "$DEPLOY_PORT")
fi

shell_quote() {
  printf "%q" "$1"
}

REMOTE_ENV=$(
  printf "DEPLOY_DIR=%s " "$(shell_quote "$DEPLOY_DIR")"
  printf "DEPLOY_BRANCH=%s " "$(shell_quote "$DEPLOY_BRANCH")"
  printf "DEPLOY_REMOTE=%s " "$(shell_quote "$DEPLOY_REMOTE")"
  printf "DEPLOY_SHA=%s " "$(shell_quote "$DEPLOY_SHA")"
  printf "IMAGE_NAME=%s " "$(shell_quote "$IMAGE_NAME")"
  printf "DOCKERFILE=%s " "$(shell_quote "$DOCKERFILE")"
  printf "SERVICE=%s " "$(shell_quote "$SERVICE")"
  printf "COMPOSE_FILE=%s " "$(shell_quote "$COMPOSE_FILE")"
  printf "VITE_BASE_PATH=%s " "$(shell_quote "$VITE_BASE_PATH")"
  printf "VITE_API_BASE_URL=%s " "$(shell_quote "$VITE_API_BASE_URL")"
  printf "HEALTHCHECK_URL=%s" "$(shell_quote "$HEALTHCHECK_URL")"
)

echo "=== Deploying $DEPLOY_BRANCH to $SSH_TARGET:$DEPLOY_DIR ==="
if [ -n "$DEPLOY_SHA" ]; then
  echo "Target commit: $DEPLOY_SHA"
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "$REMOTE_ENV bash -se" < "$script_dir/scripts/deploy-local.sh"
