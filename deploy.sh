#!/bin/bash
# Deploy to self-hosted — multi-stage docker build runs on the server
set -e

HOST="self-hosted"
DIR="~/vfs-link"
IMAGE_NAME="vfs-link/ftp-server"
DOCKERFILE="apps/ftp-server/Dockerfile"
SERVICE="ftp-server"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.self-hosted.yml}"

echo "=== Pulling latest code on $HOST ==="
ssh "$HOST" "cd $DIR && git pull --ff-only"

echo "=== Resolving git SHA on $HOST ==="
GIT_SHA=$(ssh "$HOST" "cd $DIR && git rev-parse --short HEAD")
echo "Building image tag: $IMAGE_NAME:$GIT_SHA (also tagged as latest)"

echo "=== Building Docker image on $HOST (multi-stage, with layer cache) ==="
ssh "$HOST" "cd $DIR && docker build -f $DOCKERFILE -t $IMAGE_NAME:$GIT_SHA -t $IMAGE_NAME:latest ."

echo "=== Recreating service ==="
ssh "$HOST" "cd $DIR && docker compose -f $COMPOSE_FILE up -d --force-recreate $SERVICE"

echo "=== Done! Checking status ==="
sleep 5
ssh "$HOST" "cd $DIR && docker compose -f $COMPOSE_FILE ps"
