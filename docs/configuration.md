# Configuration reference

Copy `.env.example` to `.env`. The `.env` file is deliberately ignored by Git.

| Variable | Required | Description |
| --- | --- | --- |
| `POSTGRES_USER` | Compose | PostgreSQL role used by the bundled database. |
| `POSTGRES_PASSWORD` | Compose | Strong password for the bundled database. |
| `POSTGRES_DB` | Compose | Database name. |
| `FTP_USER`, `FTP_PASS` | Yes | FTP login credentials. Use a unique, strong password. |
| `FTP_PORT` | No | FTP control port; default `21`. |
| `HTTP_PORT` | No | HTTP API port; default `8080`. |
| `FTP_PASV_URL` | Yes | Public DNS name or IP advertised to passive FTP clients. |
| `FTP_PASV_MIN`, `FTP_PASV_MAX` | No | Inclusive passive FTP port range. |
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
