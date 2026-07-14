# vfs-link

vfs-link is a PostgreSQL-backed file server with WebDAV over HTTPS, a browser
API, optional Google Cloud Storage (GCS), and transitional FTP support. Logical
file moves only update the database mapping; object bytes are not copied during
a rename.

> [!WARNING]
> WebDAV uses Basic authentication and rejects requests that are not HTTPS.
> Terminate TLS at a managed ingress or reverse proxy, and enable forwarded
> headers only when that trusted ingress overwrites them. The browser API remains unauthenticated and FTP is plaintext,
> so keep both behind access control, a VPN, or a private network.

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
Browser ─────── HTTP(S) ─┼─> file-server ─> PostgreSQL (paths + DAV locks)
FTP client ────── FTP ───┘       │
                                 ├────────> local object volume
                                 └────────> GCS (serverless primary storage)
```

The server provides:

- WebDAV operations including range downloads, streaming uploads, moves,
  copies, directories, and PostgreSQL-backed `LOCK`/`UNLOCK`.
- Optional FTP operations during the migration period.
- A read-only file browser API and bundled React UI.
- Optional GCS-backed primary storage and GCS/Telegram file sharing.
- Mapping rebuild and physical-object health-check tools.

For Cloud Run or a similar scale-to-zero HTTP platform, use GCS plus an external
PostgreSQL database, set `FTP_ENABLED=false`, trust only the platform-managed
forwarded headers, and expose only the HTTP port.
Standard WebDAV `PUT` streams through one request; resumable direct-to-GCS
uploads for custom clients are outside the current protocol endpoint.

## Documentation

- [Self-hosting](docs/self-hosting.md)
- [Configuration reference](docs/configuration.md)
- [Storage and GCS](docs/storage.md)
- [Networking and exposure](docs/networking.md)
- [WebDAV and serverless deployment](docs/webdav.md)
- [Operations](docs/operations.md)
- [Development](docs/development.md)

## License and security

Licensed under [MIT](LICENSE). Please follow [SECURITY.md](SECURITY.md) for
private vulnerability reports, and [CONTRIBUTING.md](CONTRIBUTING.md) for
development guidelines.
