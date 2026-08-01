#!/bin/sh
set -e

echo "Starting vfs-link File Server..."

db_driver="${DB_DRIVER:-postgres}"
case "$db_driver" in
  postgres)
    if [ -z "$DATABASE_URL" ]; then
      echo "Error: DATABASE_URL is not set for DB_DRIVER=postgres."
      exit 1
    fi
    ;;
  json)
    metadata_driver="${METADATA_STORAGE_DRIVER:-local}"
    case "$metadata_driver" in
      local)
        if [ -z "$METADATA_LOCAL_ROOT" ]; then
          echo "Error: METADATA_LOCAL_ROOT is not set for METADATA_STORAGE_DRIVER=local."
          exit 1
        fi
        ;;
      gcs)
        if [ -z "$METADATA_GCS_BUCKET" ]; then
          echo "Error: METADATA_GCS_BUCKET is not set for METADATA_STORAGE_DRIVER=gcs."
          exit 1
        fi
        ;;
      *)
        echo "Error: unsupported METADATA_STORAGE_DRIVER '$metadata_driver'."
        exit 1
        ;;
    esac
    ;;
  *)
    echo "Error: unsupported DB_DRIVER '$db_driver'."
    exit 1
    ;;
esac

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
