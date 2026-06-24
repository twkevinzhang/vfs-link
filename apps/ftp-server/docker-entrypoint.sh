#!/bin/sh
set -e

echo "Starting vfs-link FTP Server..."

# 檢查 DATABASE_URL
if [ -z "$DATABASE_URL" ]; then
  echo "Error: DATABASE_URL is not set."
  exit 1
fi

if [ -z "$LOCAL_STORAGE_ROOT" ]; then
  echo "Error: LOCAL_STORAGE_ROOT is not set."
  exit 1
fi

exec ./ftp-server "$@"
