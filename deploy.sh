#!/usr/bin/env bash
# Deploy to self-hosted. Docker build still runs on the server so production
# secrets and local object storage stay on the host.
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
  printf "HEALTHCHECK_URL=%s" "$(shell_quote "$HEALTHCHECK_URL")"
)

echo "=== Deploying $DEPLOY_BRANCH to $SSH_TARGET:$DEPLOY_DIR ==="
if [ -n "$DEPLOY_SHA" ]; then
  echo "Target commit: $DEPLOY_SHA"
fi

ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "$REMOTE_ENV bash -se" <<'REMOTE_SCRIPT'
set -euo pipefail

expand_dir() {
  case "$1" in
    "~")
      printf "%s\n" "$HOME"
      ;;
    "~/"*)
      printf "%s/%s\n" "$HOME" "${1#~/}"
      ;;
    *)
      printf "%s\n" "$1"
      ;;
  esac
}

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

check_url() {
  url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS "$url" >/dev/null
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -qO- "$url" >/dev/null
    return
  fi
  return 127
}

DEPLOY_DIR="$(expand_dir "$DEPLOY_DIR")"
cd "$DEPLOY_DIR"

current_branch="$(git branch --show-current)"
if [ "$current_branch" != "$DEPLOY_BRANCH" ]; then
  fail "remote checkout is on '$current_branch', expected '$DEPLOY_BRANCH'"
fi

if [ -n "$(git status --porcelain)" ]; then
  git status --short
  fail "remote checkout has uncommitted changes"
fi

upstream="$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null || true)"
if [ -n "$DEPLOY_REMOTE" ]; then
  fetch_remote="$DEPLOY_REMOTE"
  fetch_branch="$DEPLOY_BRANCH"
  target_ref="refs/remotes/$DEPLOY_REMOTE/$DEPLOY_BRANCH"
elif [ -n "$upstream" ]; then
  fetch_remote="${upstream%%/*}"
  fetch_branch="${upstream#*/}"
  target_ref="refs/remotes/$fetch_remote/$fetch_branch"
else
  remote_count="$(git remote | wc -l | tr -d ' ')"
  if [ "$remote_count" = "1" ]; then
    fetch_remote="$(git remote)"
  else
    fetch_remote="origin"
  fi
  fetch_branch="$DEPLOY_BRANCH"
  target_ref="refs/remotes/$fetch_remote/$DEPLOY_BRANCH"
fi

echo "=== Fetching $fetch_remote/$fetch_branch ==="
git fetch --prune "$fetch_remote" "+refs/heads/$fetch_branch:$target_ref"

if [ -n "$DEPLOY_SHA" ]; then
  target_commit="$(git rev-parse --verify "$DEPLOY_SHA^{commit}")"
  remote_tip="$(git rev-parse --verify "$target_ref^{commit}")"
  if ! git merge-base --is-ancestor "$target_commit" "$remote_tip"; then
    fail "target commit $DEPLOY_SHA is not contained in $target_ref"
  fi
else
  target_commit="$(git rev-parse --verify "$target_ref^{commit}")"
fi

echo "=== Fast-forwarding $DEPLOY_BRANCH to $target_commit ==="
git merge --ff-only "$target_commit"

git_sha="$(git rev-parse --short HEAD)"
echo "=== Building $IMAGE_NAME:$git_sha and $IMAGE_NAME:latest ==="
docker build -f "$DOCKERFILE" -t "$IMAGE_NAME:$git_sha" -t "$IMAGE_NAME:latest" .

echo "=== Validating compose config ==="
docker compose -f "$COMPOSE_FILE" config >/dev/null

echo "=== Recreating $SERVICE ==="
docker compose -f "$COMPOSE_FILE" up -d --force-recreate "$SERVICE"

echo "=== Compose status ==="
docker compose -f "$COMPOSE_FILE" ps

container_id="$(docker compose -f "$COMPOSE_FILE" ps -q "$SERVICE")"
if [ -z "$container_id" ]; then
  fail "compose did not return a container id for $SERVICE"
fi

if [ -z "$HEALTHCHECK_URL" ]; then
  http_port="$(
    docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$container_id" |
      awk -F= '$1 == "HTTP_PORT" { print $2; exit }'
  )"
  HEALTHCHECK_URL="http://127.0.0.1:${http_port:-8080}/api/status"
fi

echo "=== Checking $HEALTHCHECK_URL ==="
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if check_url "$HEALTHCHECK_URL"; then
    echo "Healthcheck passed"
    exit 0
  fi
  echo "Healthcheck attempt $attempt failed; retrying..."
  sleep 3
done

echo "=== Recent $SERVICE logs ==="
docker compose -f "$COMPOSE_FILE" logs --tail=80 "$SERVICE" || true
fail "healthcheck failed: $HEALTHCHECK_URL"
REMOTE_SCRIPT
