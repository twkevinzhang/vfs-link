# vfs-link

vfs-link is a PostgreSQL- or JSON-backed file server with a browser UI and HTTP
API, optional Google Cloud Storage (GCS), WebDAV, and transitional FTP support. Logical
file moves only update the database mapping; object bytes are not copied during
a rename.

> [!WARNING]
> WebDAV uses Basic authentication and rejects requests that are not HTTPS.
> Terminate TLS at a managed ingress or reverse proxy, and enable forwarded
> headers only when that trusted ingress overwrites them. Enable HTTP Basic Auth
> before exposing the browser/API, and keep plaintext FTP on a private network.

## Quick start

Prerequisites: Docker Engine with Docker Compose v2.

```bash
git clone https://github.com/twkevinzhang/vfs-link.git
cd vfs-link
cp .env.example .env
# Edit .env: replace every CHANGE_ME password.
docker compose up -d --build
curl -fsS http://localhost:8080/api/status
```

The default Compose configuration keeps WebDAV disabled and FTP enabled while
using PostgreSQL and a persistent local object volume. After placing port `8080`
behind an HTTPS proxy, set `WEBDAV_ENABLED=true` and
`WEBDAV_TRUST_FORWARDED_HEADERS=true`, then connect a client to
`https://your-host.example/dav/` with
`WEBDAV_USER` and `WEBDAV_PASS`. The FTP control port is `21`; passive data
ports are `30000-30100`. For HTTP-only operation, set `FTP_ENABLED=false`.

## Architecture

```text
WebDAV client ── HTTPS ──┐
Browser ─────── HTTP(S) ─┼─> file-server ─> PostgreSQL or JSON metadata tree
FTP client ────── FTP ───┘       │
                                 ├────────> local object volume
                                 └────────> GCS (serverless primary storage)
```

The server provides:

- WebDAV operations including range downloads, streaming uploads, moves,
  copies, directories, and PostgreSQL-backed `LOCK`/`UNLOCK`.
- Optional FTP operations during the migration period.
- A file browser API and bundled React UI with local or direct-to-GCS uploads.
- Optional GCS-backed primary storage and GCS/Telegram file sharing.
- Optional Pub/Sub dispatch for reliable serverless share jobs.
- Mapping rebuild and physical-object health-check tools.

For Cloud Run or a similar scale-to-zero HTTP platform, use GCS plus either an
external PostgreSQL database or `DB_DRIVER=json`, set `FTP_ENABLED=false`, and
expose only the HTTP port. Keep file bytes in the primary bucket and the JSON
metadata tree in a separate Standard-class bucket in the Cloud Run region.
JSON metadata uses GCS generation preconditions so concurrent instances cannot
silently overwrite each other's updates.
Standard WebDAV `PUT` streams through one request; resumable direct-to-GCS
uploads for custom clients are outside the current protocol endpoint.

## Documentation

- [Self-hosting](docs/self-hosting.md)
- [Configuration reference](docs/configuration.md)
- [Storage and GCS](docs/storage.md)
- [Legacy JSON metadata migration](docs/metadata-migration.md)
- [Networking and exposure](docs/networking.md)
- [WebDAV and serverless deployment](docs/webdav.md)
- [Browser upload API](docs/uploads.md)
- [Operations, move, and trash API](docs/operations.md)
- [Cloud Run HTTP file-server](docs/cloud-run.md)
- [Operations](docs/operations.md)
- [Development](docs/development.md)

## License and security

Licensed under [MIT](LICENSE). Please follow [SECURITY.md](SECURITY.md) for
private vulnerability reports, and [CONTRIBUTING.md](CONTRIBUTING.md) for
development guidelines.
