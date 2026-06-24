# vfs-link (local-first Go FTP Server)

`vfs-link` is a PostgreSQL-backed FTP server that stores physical file bytes in a local object store while exposing a virtual FTP path tree from the database.

The key design goal is fast logical movement: FTP `RNFR` / `RNTO` operations update database `logicPath` values only. Physical files are not copied or renamed during logical moves.

## Version Notes

This branch is **v3**, a local-first evolution of the v2 Go rewrite.

- v1: Node.js / TypeScript + `ftp-srv` + Prisma + GCS.
- v2: Go FTP server + PostgreSQL mapping + GCS object storage.
- v3: Go FTP server + PostgreSQL mapping + local object storage + local React browser.

See [docs/v1-v2-differences.md](docs/v1-v2-differences.md) for the v1/v2 comparison. v3 keeps the v2 Go runtime shape and replaces GCS with local storage by default.

## Architecture

- **Runtime**: Go 1.23+
- **FTP engine**: `github.com/fclairamb/ftpserverlib`
- **Database**: PostgreSQL
- **Storage**: local object store under `LOCAL_STORAGE_ROOT`
- **HTTP API**: read-only file browsing API on `HTTP_PORT`
- **Web UI**: React local file browser under `apps/web`
- **Deployment**: Docker Compose

## Data Model

The Go server creates the mapping table on startup if it does not already exist:

```sql
CREATE TABLE IF NOT EXISTS "File" (
  id SERIAL PRIMARY KEY,
  "logicPath" TEXT NOT NULL UNIQUE,
  "physicalHash" TEXT NOT NULL,
  size BIGINT NOT NULL DEFAULT 0,
  "isDirectory" BOOLEAN NOT NULL DEFAULT false,
  "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS "File_logicPath_idx" ON "File" ("logicPath");
```

`logicPath` is the user-facing FTP path. `physicalHash` is the local object key stored under `LOCAL_STORAGE_ROOT`.

## Core Behavior

- `LIST` / `MLSD`: reads children from PostgreSQL and returns only the direct entries for the requested directory.
- `RETR`: resolves `logicPath` to `physicalHash` and streams the local object file to the FTP data connection.
- `STOR`: streams upload data into a local object file, upserts the mapping after the upload closes cleanly, and removes the replaced object after a successful overwrite.
- `RNFR` / `RNTO`: updates database paths only; local object names stay unchanged.
- `DELE` / `RMD`: deletes local object files for files and removes their database mappings.
- `MKD`: creates directory mappings in PostgreSQL.
- HTTP API: exposes read-only browsing endpoints for the local React UI.

## HTTP API

- `GET /api/status`: storage driver, storage root, and aggregate file stats.
- `GET /api/files?path=/`: direct children for a logical directory.
- `GET /api/tree`: full logical directory tree.
- `GET /api/download?path=/docs/a.pdf`: downloads a logical file.
- `POST /api/shares/drafts`: creates a GCS share draft for a logical file.
- `GET /api/shares/{id}`: reads a share job.
- `POST /api/shares/{id}/start`: uploads the file to GCS and sends email when configured.

## Local React Browser

The browser app lives in `apps/web` and reads the Go server's HTTP API. It does not connect to PostgreSQL or `LOCAL_STORAGE_ROOT` directly.

Start the API/FTP server locally:

```bash
docker compose up -d db

cd apps/ftp-server
DATABASE_URL='postgresql://postgres:postgres@localhost:5434/vfs_link?sslmode=disable' \
LOCAL_STORAGE_ROOT='../../data/objects' \
FTP_PORT=2121 \
HTTP_PORT=8080 \
./scripts/go.sh run ./cmd/ftp-server
```

Start the React browser:

```bash
pnpm --dir apps/web dev
```

Open `http://localhost:5173`. If the API is not on `http://localhost:8080`, set `VITE_API_BASE_URL` before starting the web app:

```bash
VITE_API_BASE_URL='http://localhost:18080' pnpm --dir apps/web dev
```

Build the web app:

```bash
pnpm --dir apps/web build
```

## File Sharing

v3 remains local-first for FTP storage. File sharing is an export workflow: the Go server reads the local object, uploads it to the configured GCS bucket, then optionally sends the share link by email.

Required for sharing:

- `SHARE_GCS_BUCKET`: destination GCS bucket
- `GOOGLE_APPLICATION_CREDENTIALS`: service account credentials, or another ADC source available to the Go process

Optional:

- `SHARE_GCS_PREFIX`: object prefix, defaults to `shares`
- `SHARE_PUBLIC_BASE_URL`: public base URL for generated links; defaults to `https://storage.googleapis.com/{bucket}`
- `SMTP_HOST`: SMTP host for email notifications
- `SMTP_PORT`: SMTP port, defaults to `587`
- `SMTP_USER`: SMTP username
- `SMTP_PASS`: SMTP password
- `SMTP_FROM`: sender address

When `SMTP_HOST` or `SMTP_FROM` is missing, uploads still complete but email delivery for requested recipients is marked as `email_failed`.

## Environment

Required:

- `DATABASE_URL`: PostgreSQL connection string
- `LOCAL_STORAGE_ROOT`: local object storage root

Optional:

- `STORAGE_DRIVER`: storage driver, currently only `local`, defaults to `local`
- `FTP_USER`: FTP username, defaults to `admin`
- `FTP_PASS`: FTP password, defaults to `admin123`
- `FTP_PORT`: FTP control port, defaults to `21`
- `HTTP_PORT`: read-only HTTP API port, defaults to `8080`
- `FTP_PASV_URL`: passive mode public host/IP, defaults to `127.0.0.1`
- `FTP_PASV_MIN`: passive port range start, defaults to `30000`
- `FTP_PASV_MAX`: passive port range end, defaults to `30005`
- `SHARE_GCS_BUCKET`: GCS bucket for file sharing
- `SHARE_GCS_PREFIX`: GCS object prefix for shares, defaults to `shares`
- `SHARE_PUBLIC_BASE_URL`: optional public URL prefix for share links
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SMTP_FROM`: optional email notification settings

## Local Build

```bash
cd apps/ftp-server
./scripts/go.sh build ./cmd/ftp-server
```

## Docker

Build and start through the existing Nx target:

```bash
npx nx up ftp-server
```

Or directly:

```bash
docker build -t vfs-link/ftp-server:latest -f apps/ftp-server/Dockerfile .
docker compose up -d
```

Docker Compose exposes the read-only API on `${HTTP_PORT:-8080}` and persists local object bytes in the `objectdata` named volume.

For self-hosted deployment, use `docker-compose.self-hosted.yml`. It keeps the server on host networking, reads the existing external `DATABASE_URL`, mounts the existing `./.auth/gcp-key.json` into `/app/gcp-key.json`, and stores local-first object bytes under `${LOCAL_STORAGE_HOST_PATH:-./data/objects}`. `deploy.sh` uses this compose file by default.

### GitHub Actions deployment

Pushes to `main` run `.github/workflows/deploy-self-hosted.yml`. The workflow runs Go tests, builds the FTP server image as a CI check, then SSHes into self-hosted and runs `deploy.sh`.

Configure these repository or environment secrets before enabling the deployment job:

- `SELF_HOSTED_HOST`: self-hosted SSH host or IP address.
- `SELF_HOSTED_SSH_PRIVATE_KEY`: private key allowed to SSH into self-hosted.
- `SELF_HOSTED_USER`: SSH user. Defaults to `self-hosted` when omitted.
- `SELF_HOSTED_SSH_PORT`: SSH port. Defaults to `22` when omitted.
- `SELF_HOSTED_DEPLOY_DIR`: remote checkout directory. Defaults to `~/vfs-link` when omitted.
- `SELF_HOSTED_KNOWN_HOSTS`: optional pinned known_hosts entry. When omitted, the workflow uses `ssh-keyscan`.
- `SELF_HOSTED_HEALTHCHECK_URL`: optional healthcheck URL. When omitted, `deploy.sh` checks `http://127.0.0.1:${HTTP_PORT:-8080}/api/status` on self-hosted.

## Rebuild Mapping Table

To rebuild logical mappings from object files currently present in `LOCAL_STORAGE_ROOT`:

```bash
docker exec -it vfs-link ./ftp-server rebuild-mapping
```

Pass `--yes` to skip the 5-second countdown:

```bash
docker exec -it vfs-link ./ftp-server rebuild-mapping --yes
```
