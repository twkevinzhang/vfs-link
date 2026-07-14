# Configuration reference

Copy `.env.example` to `.env`. The `.env` file is deliberately ignored by Git.

| Variable | Required | Description |
| --- | --- | --- |
| `POSTGRES_USER` | Compose | PostgreSQL role used by the bundled database. |
| `POSTGRES_PASSWORD` | Compose | Strong password for the bundled database. |
| `POSTGRES_DB` | Compose | Database name. |
| `FTP_ENABLED` | No | Enables transitional FTP service; default `true`. Set `false` for HTTP-only/serverless operation. |
| `FTP_USER`, `FTP_PASS` | FTP enabled | FTP login credentials. Use a unique, strong password. |
| `FTP_PORT` | No | FTP control port; default `21`. |
| `HTTP_PORT` | No | WebDAV, browser, and HTTP API port; falls back to platform `PORT`, then `8080`. |
| `FTP_PASV_URL` | Yes | Public DNS name or IP advertised to passive FTP clients. |
| `FTP_PASV_MIN`, `FTP_PASV_MAX` | No | Inclusive passive FTP port range. |
| `WEBDAV_ENABLED` | No | Mounts the WebDAV endpoint; default `false`. |
| `WEBDAV_PATH` | No | WebDAV URL prefix; default `/dav/`; `/` and `/api/` overlap are rejected. |
| `WEBDAV_USER`, `WEBDAV_PASS` | WebDAV enabled | Basic Auth app credentials. HTTPS is mandatory. |
| `WEBDAV_LOCK_TIMEOUT` | No | Maximum/default PostgreSQL-backed DAV lock duration; default `30m`. |
| `WEBDAV_TRUST_FORWARDED_HEADERS` | No | Accept `X-Forwarded-Proto=https`; default `false`. Enable only behind a trusted ingress that overwrites the header. |
| `STORAGE_DRIVER` | No | `local` (default) or `gcs`. |
| `GCS_BUCKET` | GCS primary storage | Bucket for primary file bytes. |
| `SHARE_GCS_BUCKET` | Sharing | Destination bucket for exported shares. |
| `SHARE_GCS_PREFIX` | No | Object prefix for share exports; default `shares`. |
| `SHARE_PUBLIC_BASE_URL` | No | Public base URL used in generated share links. |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | Notification | Optional Telegram delivery settings. Uploads still finish without them. |
| `GOOGLE_APPLICATION_CREDENTIALS_HOST` | GCS Compose overlay | Absolute host path to a service-account credential file. |

`GCS_BUCKET` and `SHARE_GCS_BUCKET` serve different purposes. Selecting GCS as
the primary store does not automatically configure file sharing, and changing a
storage driver does not migrate existing files.

For a binary started outside Docker, set `DATABASE_URL` and `LOCAL_STORAGE_ROOT`
when `STORAGE_DRIVER=local`; set `DATABASE_URL` and `GCS_BUCKET` when it is
`gcs`. `WEB_STATIC_ROOT` optionally serves the built browser UI and
`WEB_BASE_PATH` sets its public path prefix.

For Cloud Run or another stateless HTTP platform, set `FTP_ENABLED=false`,
`WEBDAV_ENABLED=true`, and `STORAGE_DRIVER=gcs`; supply `DATABASE_URL`, GCS
access, and the WebDAV app password through managed secrets. TLS normally
terminates at the platform ingress, which must forward
`X-Forwarded-Proto: https`.
