# vfs-link (Go FTP Server)

`vfs-link` is a PostgreSQL-backed FTP server that stores physical file bytes in Google Cloud Storage while exposing a virtual FTP path tree from the database.

The key design goal is fast logical movement: FTP `RNFR` / `RNTO` operations update database `logicPath` values only. GCS objects are not copied or renamed during logical moves.

## Architecture

- **Runtime**: Go 1.23+
- **FTP engine**: `github.com/fclairamb/ftpserverlib`
- **Database**: PostgreSQL
- **Storage**: Google Cloud Storage
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

## Core Behavior

- `LIST` / `MLSD`: reads children from PostgreSQL and returns only the direct entries for the requested directory.
- `RETR`: resolves `logicPath` to `physicalHash` and streams the GCS object to the FTP data connection.
- `STOR`: streams upload data into a new GCS object and upserts the mapping after the upload closes cleanly.
- `RNFR` / `RNTO`: updates database paths only; physical GCS object names stay unchanged.
- `DELE` / `RMD`: deletes GCS objects for files and removes their database mappings.
- `MKD`: creates directory mappings in PostgreSQL.

## Environment

Required:

- `DATABASE_URL`: PostgreSQL connection string
- `GCS_BUCKET`: Google Cloud Storage bucket name
- `GOOGLE_APPLICATION_CREDENTIALS`: service account JSON path when not using workload identity

Optional:

- `FTP_USER`: FTP username, defaults to `admin`
- `FTP_PASS`: FTP password, defaults to `admin123`
- `FTP_PORT`: FTP control port, defaults to `21`
- `FTP_PASV_URL`: passive mode public host/IP, defaults to `127.0.0.1`
- `FTP_PASV_MIN`: passive port range start, defaults to `30000`
- `FTP_PASV_MAX`: passive port range end, defaults to `30005`

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

## Rebuild Mapping Table

To rebuild logical mappings from the objects currently present in GCS:

```bash
docker exec -it vfs-link ./ftp-server rebuild-mapping
```

Pass `--yes` to skip the 5-second countdown:

```bash
docker exec -it vfs-link ./ftp-server rebuild-mapping --yes
```
