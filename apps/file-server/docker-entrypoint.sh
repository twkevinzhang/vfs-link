#!/bin/sh
set -e

echo "Starting vfs-link File Server..."

# 檢查 DATABASE_URL
if [ -z "$DATABASE_URL" ]; then
  echo "Error: DATABASE_URL is not set."
  exit 1
fi

storage_driver="${STORAGE_DRIVER:-local}"
case "$storage_driver" in
  local)
    if [ -z "$LOCAL_STORAGE_ROOT" ]; then
      echo "Error: LOCAL_STORAGE_ROOT is not set for STORAGE_DRIVER=local."
      exit 1
    fi
    ;;
  gcs)
    if [ -z "$GCS_BUCKET" ]; then
      echo "Error: GCS_BUCKET is not set for STORAGE_DRIVER=gcs."
      exit 1
    fi
    ;;
  *)
    echo "Error: unsupported STORAGE_DRIVER '$storage_driver'."
    exit 1
    ;;
esac

exec ./file-server "$@"
