#!/bin/sh
set -e

echo "Starting Docker Entrypoint..."

# 檢查 DATABASE_URL
if [ -z "$DATABASE_URL" ]; then
  echo "Error: DATABASE_URL is not set."
  exit 1
fi

echo "Generating Prisma Client..."
npx prisma generate --schema=./prisma/schema.prisma

# 執行 Prisma 生產環境同步
echo "Syncing database schema with Prisma..."
npx prisma db push --schema=./prisma/schema.prisma

# 啟動應用程式
echo "Starting FTP Server..."
exec node dist/index.js
