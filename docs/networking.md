# Networking and exposure

WebDAV uses Basic authentication and requires HTTPS. The process expects TLS to
terminate at a managed ingress or reverse proxy. Forwarded HTTPS is accepted
only when `WEBDAV_TRUST_FORWARDED_HEADERS=true`; enable it only when the trusted
ingress overwrites `X-Forwarded-Proto` and the backend is not directly exposed.
The browser API has no built-in authentication, and the
transitional FTP endpoint is plaintext.

## Required ports

| Port | Protocol | Purpose |
| --- | --- | --- |
| `8080` | TCP | WebDAV, browser, and HTTP API behind HTTPS ingress |
| `21` | TCP | Transitional FTP control connection when enabled |
| `30000-30100` | TCP | Transitional FTP passive data range when enabled |

If the service is behind NAT, `FTP_PASV_URL` must resolve to an address reachable
by the client, and every passive port must be forwarded to the container host.
The control port alone is not sufficient.

The reverse proxy must preserve WebDAV methods and the `Authorization`,
`Destination`, `Depth`, `If`, `Lock-Token`, and range headers. Do not rely on
the web UI as an access-control boundary:
the file listing and download endpoints are unauthenticated. The status endpoint
also reveals operational details and should remain private.

When using a path prefix with a reverse proxy, build the web assets with matching
`VITE_BASE_PATH` and `VITE_API_BASE_URL` build arguments. The API base defaults
to the browser's current origin and root path. Set `VITE_API_BASE_URL` when the
API uses either a different origin or a path prefix on the same origin.

| Deployment | `VITE_BASE_PATH` | `VITE_API_BASE_URL` | Files request |
| --- | --- | --- | --- |
| GCP | `/` | empty | `/api/files` |
| ipproxy | `/vfs-link/viewer` | `/vfs-link` | `/vfs-link/api/files` |

These are build-time values embedded in the web assets, not runtime container
environment overrides. Test browser refreshes, API requests, and download URLs
through the final public proxy path.

An HTTP-only serverless deployment listens only on the platform-provided HTTP
port with `FTP_ENABLED=false`. Use GCS instead of the ephemeral local filesystem
and an external PostgreSQL database so requests can move between instances.
