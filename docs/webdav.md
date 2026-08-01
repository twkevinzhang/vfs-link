# WebDAV over HTTPS

The file server mounts WebDAV at `/dav/` by default. It supports ordinary DAV
file and directory operations, HTTP range downloads, streaming `PUT`, and
PostgreSQL-backed locks shared by every server instance.

## Enable the endpoint

```dotenv
WEBDAV_ENABLED=true
WEBDAV_PATH=/dav/
WEBDAV_USER=vfs_link
WEBDAV_PASS=a-unique-app-password
WEBDAV_LOCK_TIMEOUT=30m
WEBDAV_TRUST_FORWARDED_HEADERS=true
```

WebDAV Basic Auth credentials are independent from FTP credentials. The handler
rejects plaintext requests, so connect through an HTTPS ingress or reverse
proxy. Set `WEBDAV_TRUST_FORWARDED_HEADERS=true` only when that ingress is the
only route to the backend and overwrites (rather than appends to)
`X-Forwarded-Proto: https`. It must preserve the
`Authorization` header, and allow WebDAV methods such as `PROPFIND`, `MKCOL`,
`COPY`, `MOVE`, `LOCK`, and `UNLOCK`.

The endpoint rejects unbounded `PROPFIND Depth: infinity`, cross-host or
out-of-prefix destinations, and changes to the DAV root. Use `Depth: 0` or
`Depth: 1` for discovery.

## Serverless HTTP mode

For Cloud Run or a similar stateless HTTP container platform:

```dotenv
FTP_ENABLED=false
WEBDAV_ENABLED=true
WEBDAV_TRUST_FORWARDED_HEADERS=true
STORAGE_DRIVER=gcs
GCS_BUCKET=your-primary-object-bucket
```

Provide `DATABASE_URL`, `WEBDAV_USER`, and `WEBDAV_PASS` through managed
secrets. Give the runtime identity access to the primary GCS bucket and database.
Expose only `HTTP_PORT` through the managed ingress; do not make the backend
port directly reachable. The ingress terminates TLS. Do not use local
storage because an instance filesystem is ephemeral and is not shared between
instances.

All lock state lives in PostgreSQL, so locks survive instance replacement and
remain visible across concurrent instances. File bytes live in GCS, and the
database publishes the logical mapping only after a streamed upload closes
successfully.

## Large files

Range downloads are supported by both local and GCS storage adapters. A WebDAV
`PUT` is one streaming HTTP request and does not provide cross-request resumable
uploads. Configure the platform request timeout for the expected file size and
network speed. A future browser or custom client may use a separate resumable
direct-to-GCS flow; that is not part of the standard WebDAV endpoint.

## FTP transition

Leave `FTP_ENABLED=true` while legacy clients migrate, then set it to `false`
once WebDAV is accepted. FTP continues to use `FTP_USER`, `FTP_PASS`, the
control port, and the passive port range. Disabling FTP does not change paths,
objects, or PostgreSQL mappings used by WebDAV.
