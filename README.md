# vfs-link (Go FTP Server)

`vfs-link` is a PostgreSQL-backed FTP server that stores physical file bytes in a selectable local or Google Cloud Storage object store while exposing a virtual FTP path tree from the database.

The key design goal is fast logical movement: FTP `RNFR` / `RNTO` operations update database `logicPath` values only. Physical files are not copied or renamed during logical moves.

## Version Notes

This branch is **v3**, an evolution of the v2 Go rewrite with a storage adapter boundary and a React browser.

- v1: Node.js / TypeScript + `ftp-srv` + Prisma + GCS.
- v2: Go FTP server + PostgreSQL mapping + GCS object storage.
- v3: Go FTP server + PostgreSQL mapping + selectable local/GCS object storage + React browser.

See [docs/v1-v2-differences.md](docs/v1-v2-differences.md) for the v1/v2 comparison. v3 keeps the v2 Go runtime shape, defaults to local storage, and can use GCS as the active primary store.

## Architecture

- **Runtime**: Go 1.23+
- **FTP engine**: `github.com/fclairamb/ftpserverlib`
- **Database**: PostgreSQL
- **Storage**: `local` under `LOCAL_STORAGE_ROOT`, or `gcs` in `GCS_BUCKET`
- **HTTP API**: read-only file browsing API on `HTTP_PORT`
- **Web UI**: React file browser under `apps/web`
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

`logicPath` is the user-facing FTP path. `physicalHash` is the object key in the active primary store.

## Core Behavior

- `LIST` / `MLSD`: reads children from PostgreSQL and returns only the direct entries for the requested directory.
- `RETR`: resolves `logicPath` to `physicalHash` and streams the object from the active store to the FTP data connection.
- `STOR`: streams upload data into the active store, upserts the mapping after the upload closes cleanly, and removes the replaced object after a successful overwrite.
- `RNFR` / `RNTO`: updates database paths only; physical object names stay unchanged.
- `DELE` / `RMD`: deletes physical objects for files and removes their database mappings.
- `MKD`: creates directory mappings in PostgreSQL.
- HTTP API: exposes read-only browsing endpoints for the React UI.

## HTTP API

- `GET /api/status`: storage driver, storage root, and aggregate file stats.
- `GET /api/files?path=/&limit=100&offset=0&q=term`: paginated direct children for a logical directory; `q` filters within the current directory.
- `GET /api/tree?path=/`: direct child folders for one logical directory.
- `GET /api/download?path=/docs/a.pdf`: downloads a logical file.
- `POST /api/shares/drafts`: creates a GCS share draft for a logical file.
- `GET /api/shares/{id}`: reads a share job.
- `POST /api/shares/{id}/start`: uploads the file to GCS and sends a Telegram notification when configured.

## React Browser

The browser app lives in `apps/web` and reads the Go server's HTTP API. It does not connect to PostgreSQL or the active object store directly.

Start the API/FTP server locally:

```bash
docker compose up -d db

cd apps/ftp-server
DATABASE_URL='postgresql://postgres:postgres@localhost:5434/vfs_link?sslmode=disable' \
STORAGE_DRIVER='local' \
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

When the web app is deployed behind a path gateway, build it with the public
base path and the API gateway root:

```bash
VITE_BASE_PATH='/vfs-link/index' VITE_API_BASE_URL='/vfs-link' pnpm --dir apps/web build
```

## File Sharing

File sharing is an export workflow independent from the primary storage driver. The Go server reads the object from the active local or GCS store, uploads it to `SHARE_GCS_BUCKET`, then sends the share link through Telegram when configured. `GCS_BUCKET` and `SHARE_GCS_BUCKET` may point to different buckets.

Required for sharing:

- `SHARE_GCS_BUCKET`: destination GCS bucket
- `GOOGLE_APPLICATION_CREDENTIALS`: service account credentials, or another ADC source available to the Go process

Optional:

- `SHARE_GCS_PREFIX`: object prefix, defaults to `shares`
- `SHARE_PUBLIC_BASE_URL`: public base URL for generated links; defaults to `https://storage.googleapis.com/{bucket}`
- `TELEGRAM_BOT_TOKEN`: Telegram bot token for share notifications
- `TELEGRAM_CHAT_ID`: Telegram channel, group, or private chat id

When `TELEGRAM_BOT_TOKEN` or `TELEGRAM_CHAT_ID` is missing, uploads still complete but notification delivery is marked as `notification_failed`.

## Environment

Required:

- `DATABASE_URL`: PostgreSQL connection string
- `LOCAL_STORAGE_ROOT`: local object storage root when `STORAGE_DRIVER=local`
- `GCS_BUCKET`: primary GCS bucket when `STORAGE_DRIVER=gcs`

Optional:

- `STORAGE_DRIVER`: `local` or `gcs`, defaults to `local`
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
- `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`: optional Telegram notification settings

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

Docker Compose exposes the read-only API on `${HTTP_PORT:-8080}`. It defaults to local storage and persists object bytes in the `objectdata` named volume; set `STORAGE_DRIVER=gcs` and `GCS_BUCKET` to select GCS, with Application Default Credentials available to the container.

For self-hosted deployment, use `docker-compose.self-hosted.yml`. It intentionally remains on `STORAGE_DRIVER=local`, keeps the server on host networking, reads the existing external `DATABASE_URL`, mounts `${SELF_HOSTED_RUNTIME_DIR}/.auth/gcp-key.json` into `/app/gcp-key.json`, and stores object bytes at `LOCAL_STORAGE_HOST_PATH`. The production runtime directory defaults to `~/vfs-link`; CD treats it only as runtime state and never fetches Git or builds source there.

The CI image includes the React browser and defaults it to `/vfs-link/index`, with API requests routed through `/vfs-link/api`.

### GitHub Actions CI/CD

The pipeline keeps CI and production deployment separate:

```text
main push
  -> CI (organization self-hosted runner): test, build, push GHCR image tagged with the commit SHA
  -> CD (organization runner labelled self-hosted): pull that immutable image and recreate ftp-server
```

`ci.yml` uses only the standard `self-hosted`, `linux`, and `x64` labels. GitHub can assign it to any eligible organization runner; it has no vfs-link-specific runner dependency. It publishes:

```text
ghcr.io/twkevinzhang/vfs-link/ftp-server:<commit-sha>
ghcr.io/twkevinzhang/vfs-link/ftp-server:latest
```

`deploy-self-hosted.yml` starts only after a successful CI run on `main`, or can be manually dispatched with an already-published commit SHA. It targets the organization runner labelled `self-hosted`, which identifies the deployment node rather than a project-specific runner. The runner must be registered at the twkevinzhang organization level and its runner group must allow this repository.

The CD runner needs Docker access and these production files on self-hosted:

- `~/vfs-link/.env`: runtime settings, including `DATABASE_URL`.
- `~/vfs-link/.auth/gcp-key.json`: GCP credential mounted read-only into the container.
- `~/vfs-link/data/objects`: local object store. Set repository variable `SELF_HOSTED_VFS_LINK_RUNTIME_DIR` when this directory is elsewhere.

Configure `SELF_HOSTED_HEALTHCHECK_URL` as an environment secret only when the default `http://127.0.0.1:${HTTP_PORT:-8080}/api/status` is unsuitable. The workflow uses its ephemeral Actions checkout for the compose definition, so deployment never fetches Git or builds an image in the self-hosted runtime directory.

## Rebuild Mapping Table

To rebuild logical mappings from objects currently present in the active primary store:

```bash
docker exec -it vfs-link ./ftp-server rebuild-mapping
```

Pass `--yes` to skip the 5-second countdown:

```bash
docker exec -it vfs-link ./ftp-server rebuild-mapping --yes
```

## Physical Hash Health Check

Use this read-only check to compare PostgreSQL `File` mappings against the
active local or GCS primary store. GCS mode checks object metadata without
downloading object bytes.

```bash
cd apps/ftp-server
./scripts/check-physical-health.sh \
  --env-file ../../.env \
  --prefix /浦城街招租 \
  --csv /tmp/vfs-link-physical-health.csv
```

Useful options:

- `--prefix /path`: limit the scan to one logical directory.
- `--storage-driver local|gcs`: override `STORAGE_DRIVER`.
- `--local-root PATH`: override `LOCAL_STORAGE_ROOT` in local mode.
- `--gcs-bucket BUCKET`: override `GCS_BUCKET` in GCS mode.
- `--google-credentials PATH`: override `GOOGLE_APPLICATION_CREDENTIALS`.
- `--fail-on-unhealthy`: exit with status `2` when any file is unhealthy.

See [`apps/ftp-server/cmd/physical-health/README.md`](apps/ftp-server/cmd/physical-health/README.md) for the complete command reference.
