#!/bin/sh
set -e

echo "Starting vfs-link FTP Server..."

# 檢查 DATABASE_URL
if [ -z "$DATABASE_URL" ]; then
  echo "Error: DATABASE_URL is not set."
  exit 1
fi

if [ -z "$GCS_BUCKET" ]; then
  echo "Error: GCS_BUCKET is not set."
  exit 1
fi

exec ./ftp-server "$@"
