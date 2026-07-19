# vfs-link

> ## Move paths, not bytes.
>
> vfs-link is a virtual filesystem for object storage. The user-visible file tree
> lives in PostgreSQL or a distributed JSON metadata tree, while file payloads
> live under opaque `physicalHash` object keys.
>
> **Rename a 100 GiB file: 0 B of payload copied.**

With `DB_DRIVER=json`, the JSON tree is not a cache—it is the source of truth
for the logical filesystem namespace. Rename, move, trash, and restore
operations change the namespace; the underlying file payload stays where it is.

## The core idea

### A path-coupled object layout

When the user-visible path is also the object key, a logical move can become a
storage operation:

```text
User-visible path                  GCS object
─────────────────                  ──────────

/incoming/report.zip  ───────────> gs://bucket/incoming/report.zip
                                    100 GiB


MOVE /incoming/report.zip
  TO /archive/report.zip


gs://bucket/incoming/report.zip
          │
          ├── copy / rewrite 100 GiB
          │
          └──────────────────────────────> gs://bucket/archive/report.zip
                                             100 GiB
          │
          └── delete the source object
```

The logical namespace and physical object layout are coupled.

### The vfs-link layout

vfs-link inserts a metadata layer between the path users see and the object
that stores the bytes:

```text
Logical namespace                         Physical storage
PostgreSQL or JSON metadata               GCS payload objects
───────────────────────────               ───────────────────

BEFORE

/incoming/report.zip
    │
    └── physicalHash: 7f3a... ──────────> gs://data-bucket/7f3a...
                                           100 GiB payload


MOVE /incoming/report.zip
  TO /archive/report.zip


AFTER

/archive/report.zip
    │
    └── physicalHash: 7f3a... ──────────> gs://data-bucket/7f3a...
                                           same 100 GiB payload
                                           same physical object

          metadata changed                  payload copied: 0 B
```

A 1 KiB file and a 100 GiB file have the same payload-copy cost during a
rename: **zero**.

For directory moves, work scales with metadata records and indexes—not with the
total number of payload bytes stored beneath that directory.

## What moves—and what does not

| Logical operation | Metadata | Physical payload |
| --- | --- | --- |
| Rename a file | Updated | Unchanged — **0 B copied** |
| Move a file | Updated | Unchanged — **0 B copied** |
| Move a directory | Updated asynchronously when needed | Unchanged — **0 B copied** |
| Move to trash | Active mapping is hidden | Retained — **0 B copied** |
| Restore from trash | Mapping is restored | Reused — **0 B copied** |
| Permanently delete | Mapping is removed | Object is deleted |

> [!IMPORTANT]
> Metadata-only does not mean zero work. Large JSON-tree operations may update
> many metadata records and run asynchronously. The important property is that
> vfs-link never rewrites the file payload merely because its logical path
> changed.

## Two buckets, two jobs

For a serverless GCS deployment, vfs-link separates frequently updated
metadata from large, mostly stable file payloads:

```text
                         Browser / HTTP API
                                  │
                                  ▼
                       ┌─────────────────────┐
                       │ vfs-link / Cloud Run│
                       └──────────┬──────────┘
                                  │
                    ┌─────────────┴─────────────┐
                    │                           │
                    ▼                           ▼
       ┌────────────────────────┐   ┌───────────────────────────┐
       │ Metadata bucket        │   │ Primary data bucket       │
       │                        │   │                           │
       │ Small JSON records     │   │ Large file payloads       │
       │ Paths and indexes      │   │ Keyed by physicalHash     │
       │ Operation manifests    │   │ Unchanged by path moves   │
       │ Aggregate statistics   │   │                           │
       │                        │   │                           │
       │ Standard storage       │   │ Standard or Archive       │
       └────────────────────────┘   └───────────────────────────┘
```

Metadata writes use GCS generation preconditions to prevent concurrent
instances from silently overwriting one another. Long-running JSON-tree
operations use persisted manifests, leases, checkpoints, and retries so they
can recover safely.

## More than cheap moves

vfs-link also provides:

- A bundled browser UI and HTTP API
- WebDAV and transitional FTP access
- PostgreSQL or distributed JSON metadata
- Local or GCS-backed primary storage
- Direct-to-GCS browser uploads
- Trash, restore, sharing, and physical-object health checks
- A serverless deployment model for Cloud Run

## Quick start

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
