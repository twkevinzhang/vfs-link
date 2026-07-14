# vfs-link

vfs-link is a PostgreSQL-backed FTP server with a browser API and optional
Google Cloud Storage (GCS) integration. Logical file moves only update the
database mapping; object bytes are not copied during a rename.

> [!WARNING]
> FTP and the HTTP API have no built-in authentication or TLS. Do not expose
> them directly to the public Internet. Put the HTTP API behind access control
> and TLS, and restrict FTP access with a firewall, VPN, or private network.

## Quick start

Prerequisites: Docker Engine with Docker Compose v2.

```bash
git clone https://github.com/twkevinzhang/vfs-link.git
cd vfs-link
cp .env.example .env
# Edit .env: set strong FTP_PASS and POSTGRES_PASSWORD values.
docker compose up -d --build
curl -fsS http://localhost:8080/api/status
```

The default Compose configuration uses PostgreSQL and a persistent local object
volume. The FTP control port is `21`; passive data ports are `30000-30100`.
For a local-only test, keep `FTP_PASV_URL=127.0.0.1`. For a remote client, set
it to the server's public DNS name or IP and open the complete passive range.

## Architecture

```text
FTP client ── FTP ──> vfs-link ──> PostgreSQL (logical-path mapping)
                         │
                         ├────────> local object volume
                         └────────> GCS (optional primary storage)

Browser ── HTTP API ───> vfs-link
```

The server provides:

- FTP operations: list, upload, download, rename, delete, and directories.
- A read-only file browser API and bundled React UI.
- Optional GCS-backed primary storage and GCS/Telegram file sharing.
- Mapping rebuild and physical-object health-check tools.

## Documentation

- [Self-hosting](docs/self-hosting.md)
- [Configuration reference](docs/configuration.md)
- [Storage and GCS](docs/storage.md)
- [Networking and exposure](docs/networking.md)
- [Operations](docs/operations.md)
- [Development](docs/development.md)

## License and security

Licensed under [MIT](LICENSE). Please follow [SECURITY.md](SECURITY.md) for
private vulnerability reports, and [CONTRIBUTING.md](CONTRIBUTING.md) for
development guidelines.
