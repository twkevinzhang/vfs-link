# Configuration reference

Copy `.env.example` to `.env`. The `.env` file is deliberately ignored by Git.

| Variable | Required | Description |
| --- | --- | --- |
| `POSTGRES_USER` | Compose | PostgreSQL role used by the bundled database. |
| `POSTGRES_PASSWORD` | Compose | Strong password for the bundled database. |
| `POSTGRES_DB` | Compose | Database name. |
| `DB_DRIVER` | No | Metadata backend: `postgres` (default) or `json`. |
| `DATABASE_URL` | PostgreSQL backend | PostgreSQL connection URL. |
| `METADATA_STORAGE_DRIVER` | JSON backend | Metadata backend: `local` (default) or `gcs`; independent from file-byte storage. |
| `METADATA_LOCAL_ROOT` | Local JSON metadata | Persistent metadata root; default `./data/metadata`. |
| `METADATA_GCS_BUCKET` | GCS JSON metadata | Dedicated Standard-class metadata bucket. |
| `METADATA_PREFIX` | JSON backend | Reserved metadata prefix. Current relative-path schema: `_vfs-link-v3`; `_vfs-link` and `_vfs-link-v2` remain readable migration sources. |
| `FTP_ENABLED` | No | Enables transitional FTP service; default `true`. Set `false` for HTTP-only/serverless operation. |
| `FTP_USER`, `FTP_PASS` | FTP enabled | FTP login credentials. Use a unique, strong password. |
| `FTP_PORT` | No | FTP control port; default `21`. |
| `HTTP_PORT` | No | WebDAV, browser, and HTTP API port; falls back to platform `PORT`, then `8080`. |
| `HTTP_BASIC_AUTH_ENABLED` | No | Protects the browser and public HTTP API; default `false`. Enable for internet exposure. |
| `HTTP_BASIC_AUTH_USER`, `HTTP_BASIC_AUTH_PASS` | HTTP auth enabled | Application Basic Auth credential. Keep the password in managed secrets. |
| `HTTP_CORS_ORIGINS` | No | Comma-separated cross-origin API allowlist. Empty means same-origin only; `*` is intended for local development. |
| `MAINTENANCE_MODE` | No | Read-only migration mode. GET/HEAD/OPTIONS remain available; metadata mutations and Pub/Sub pushes return `503`. |
| `DRIFT_ENABLED` | No | Enables GCS drift cost plans and physical reconciliation actions. Defaults to `false`; the viewer remains read-only until enabled. Use only for trusted operators. |
| `UPLOAD_MAX_BYTES` | No | Maximum declared upload size; default `53687091200` (50 GiB). |
| `UPLOAD_SESSION_TTL` | No | Upload-session lifetime; default `24h`. |
| `FTP_PASV_URL` | Yes | Public DNS name or IP advertised to passive FTP clients. |
| `FTP_PASV_MIN`, `FTP_PASV_MAX` | No | Inclusive passive FTP port range. |
| `WEBDAV_ENABLED` | No | Mounts the WebDAV endpoint; default `false`. |
| `WEBDAV_PATH` | No | WebDAV URL prefix; default `/dav/`; `/` and `/api/` overlap are rejected. |
| `WEBDAV_USER`, `WEBDAV_PASS` | WebDAV enabled | Basic Auth app credentials. HTTPS is mandatory. |
| `WEBDAV_LOCK_TIMEOUT` | No | Maximum/default PostgreSQL-backed DAV lock duration; default `30m`. |
| `WEBDAV_TRUST_FORWARDED_HEADERS` | No | Accept `X-Forwarded-Proto=https`; default `false`. Enable only behind a trusted ingress that overwrites the header. |
| `STORAGE_DRIVER` | No | `local` (default) or `gcs`. |
| `GCS_BUCKET` | GCS primary storage | Bucket for primary file bytes. |
| `THUMBNAIL_STORAGE_DRIVER` | No | `local` (default) or `gcs`; storage for derived WebP thumbnails. |
| `THUMBNAIL_LOCAL_ROOT` | Local thumbnail storage | Persistent thumbnail root; default `./data/thumbnails`, separate from primary objects. |
| `THUMBNAIL_GCS_BUCKET` | GCS thumbnail storage | Private dedicated thumbnail bucket. It is required for `THUMBNAIL_STORAGE_DRIVER=gcs` and must differ from `GCS_BUCKET`. |
| `SHARE_GCS_BUCKET` | Sharing | Destination bucket for exported shares. |
| `SHARE_GCS_PREFIX` | No | Object prefix for share exports; default `shares`. |
| `SHARE_PUBLIC_BASE_URL` | No | Public base URL used in generated share links. |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | Notification | Optional Telegram delivery settings. Uploads still finish without them. |
| `PUB_SUB_DRIVER` | No | Share job dispatcher: `goroutine` (default) or `pubsub`. |
| `GCP_PROJECT_ID`, `PUB_SUB_TOPIC` | Pub/Sub driver | Project and topic used to publish share jobs. |
| `PUB_SUB_PUSH_AUDIENCE`, `PUB_SUB_PUSH_SERVICE_ACCOUNT` | Pub/Sub driver | Expected OIDC audience and push identity for `/internal/pubsub/shares`. |
| `GOOGLE_APPLICATION_CREDENTIALS_HOST` | GCS Compose overlay | Absolute host path to a service-account credential file. |

`GCS_BUCKET` and `SHARE_GCS_BUCKET` serve different purposes. Selecting GCS as
the primary store does not automatically configure file sharing, and changing a
storage driver does not migrate existing files.

For a binary started outside Docker, select the metadata, primary-byte, and
thumbnail-byte drivers independently. PostgreSQL requires `DATABASE_URL`; JSON uses
`METADATA_STORAGE_DRIVER` with either `METADATA_LOCAL_ROOT` or
`METADATA_GCS_BUCKET`. `STORAGE_DRIVER` controls primary file bytes and
`THUMBNAIL_STORAGE_DRIVER` controls derived thumbnail bytes.
`WEB_STATIC_ROOT` optionally serves the built browser UI and
`WEB_BASE_PATH` sets its public path prefix.

For the Cloud Run HTTP file-server, set `FTP_ENABLED=false`,
`WEBDAV_ENABLED=false`, `STORAGE_DRIVER=gcs`, `THUMBNAIL_STORAGE_DRIVER=gcs`,
`DB_DRIVER=json`, and
`PUB_SUB_DRIVER=pubsub`. Enable HTTP Basic Auth and supply its password and the
Telegram bot token through Secret Manager. The Pub/Sub push route bypasses
Basic Auth and instead validates Google's OIDC token, audience, and
service-account email.
