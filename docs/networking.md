# Networking and exposure

vfs-link currently has no authentication or TLS on its HTTP API, and FTP is
plaintext. Treat both as private-network services unless you add appropriate
network and reverse-proxy controls.

## Required ports

| Port | Protocol | Purpose |
| --- | --- | --- |
| `21` | TCP | FTP control connection |
| `30000-30100` | TCP | Default FTP passive data range |
| `8080` | TCP | Browser and HTTP API |

If the service is behind NAT, `FTP_PASV_URL` must resolve to an address reachable
by the client, and every passive port must be forwarded to the container host.
The control port alone is not sufficient.

Put the HTTP API behind TLS and an authentication-capable reverse proxy, VPN, or
firewall allow-list. Do not rely on the web UI as an access-control boundary:
the file listing and download endpoints are unauthenticated. The status endpoint
also reveals operational details and should remain private.

When using a path prefix with a reverse proxy, build the web assets with matching
`VITE_BASE_PATH` and `VITE_API_BASE_URL` build arguments. Test browser refreshes
and download URLs through the final public proxy path.
