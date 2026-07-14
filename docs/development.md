# Development

Prerequisites: Go 1.23+, Node.js 22+, Corepack/pnpm, Docker Compose v2, and a
PostgreSQL instance.

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d db

cd apps/file-server
DATABASE_URL='postgresql://vfs_link:YOUR_POSTGRES_PASSWORD@localhost:5434/vfs_link?sslmode=disable' \
STORAGE_DRIVER=local \
LOCAL_STORAGE_ROOT='../../data/objects' \
FTP_ENABLED=false HTTP_PORT=8080 \
WEBDAV_ENABLED=true WEBDAV_PATH=/dav/ \
WEBDAV_USER=vfs_link WEBDAV_PASS=local-app-password \
./scripts/go.sh run ./cmd/file-server
```

In a second terminal:

```bash
pnpm --dir apps/web dev
```

Run checks before submitting changes:

```bash
cd apps/file-server && ./scripts/go.sh test ./...
pnpm --dir apps/web typecheck
pnpm --dir apps/web lint
pnpm --dir apps/web build
docker build -f apps/file-server/Dockerfile -t vfs-link/file-server:test .
```
