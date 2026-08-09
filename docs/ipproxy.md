# ipproxy deployment profile

This profile deploys a new, empty vfs-link file-server on **ipproxy**. It uses
the host-managed PostgreSQL service and Docker `services` network; it does not
create or manage either of them.

```text
/home/ipproxy/vfs-link-ipproxy
├── .env                 # host-only runtime configuration and secrets
└── data/
    ├── objects/         # new local payload store (bind-mounted at /data/objects)
    └── thumbnails/      # derived WebP store (bind-mounted at /data/thumbnails)
              │
              ▼
    vfs-link-ipproxy container ── external Docker network `services`
              │
              ▼
     PostgreSQL database vfs_link_ipproxy
```

## Database names and safety boundary

The previous aiotlab metadata database is retained only as an archive:

| Role | Database |
| --- | --- |
| Archived pre-reset index | `vfs_link_aiotlab_archive_20260801` |
| Active empty ipproxy index | `vfs_link_ipproxy` |

`DATABASE_URL` for this profile must point to `vfs_link_ipproxy`, never to the
archive database. The new `data/objects` directory starts empty, so importing
the archived index into the active database would recreate records whose old
payloads were intentionally discarded. The source `vfs_link` database is also
left unchanged as an additional safety copy.

## Runtime configuration

Keep runtime configuration outside the repository at
`/home/ipproxy/vfs-link-ipproxy/.env`. Do not store passwords, database URLs,
tokens, or image pull credentials in Git.

Set at least the following values in that host-only file (replace every angle
bracketed value):

```dotenv
IPPROXY_RUNTIME_DIR=/home/ipproxy/vfs-link-ipproxy
IPPROXY_SERVICES_NETWORK=services
VFS_LINK_IMAGE=sha256:<approved-local-image-id>
DATABASE_URL=postgresql://<role>:<password>@<postgres-service>:5432/vfs_link_ipproxy?sslmode=disable

FTP_USER=<ftp-user>
FTP_PASS=<ftp-password>
FTP_PASV_URL=<public-ftp-hostname>
FTP_PASV_MIN=30000
FTP_PASV_MAX=30100
HTTP_PORT=18080
WEB_BASE_PATH=/
VITE_BASE_PATH=/
VITE_API_BASE_URL=

HTTP_BASIC_AUTH_ENABLED=true
HTTP_BASIC_AUTH_USER=<http-user>
HTTP_BASIC_AUTH_PASS=<http-password>
HTTP_CORS_ORIGINS=<allowed-origin>

WEBDAV_ENABLED=false
MAINTENANCE_MODE=false
DRIFT_ENABLED=false
UPLOAD_SESSION_TTL=24h
UPLOAD_MAX_BYTES=53687091200
THUMBNAIL_STORAGE_DRIVER=local
THUMBNAIL_LOCAL_ROOT=/data/thumbnails
PUB_SUB_DRIVER=goroutine
```

When deploying the same release as a GCS metadata v4 cutover, follow the
ipproxy maintenance, HTTP/WebDAV/FTP acceptance, and rollback sequence in
[metadata-v4-cutover.md](metadata-v4-cutover.md). The ipproxy compose profile
continues to use its existing PostgreSQL metadata backend; it validates the
shared server/protocol implementation and does not read the GCP v4 prefix.

`VFS_LINK_IMAGE` is deliberately required and must be a content-addressed
registry digest or a verified local image ID. It is not safe to use a mutable
tag such as `latest`. The direct ipproxy build uses `VITE_BASE_PATH=/` and an
empty `VITE_API_BASE_URL`, then records the resulting local image ID in this
file. The Viewer and API are served from the `vfs-link` hostname without a
service path prefix, so browser requests such as `/api/files` remain
same-origin. These build-time values now match the GCP root-path build.
`HTTP_PORT` is the host-facing port in this profile; the application always
listens on container port `8080`. The ipproxy host publishes that port as
`18080`; the base infrastructure Nginx owns user-facing port `80` and routes
Host `vfs-link` to it.
For an HTTPS reverse proxy that terminates TLS for WebDAV, set
`WEBDAV_ENABLED=true`, configure `WEBDAV_USER` and `WEBDAV_PASS`, and set
`WEBDAV_TRUST_FORWARDED_HEADERS=true` only when that proxy overwrites
`X-Forwarded-Proto`.

The `SHARE_GCS_*`, Telegram, and Pub/Sub variables are present in the Compose
profile for compatibility. Leave them unset for this local-storage deployment
unless the corresponding external Google Cloud credentials and services have
also been explicitly configured.

## Build the ipproxy image

The GCP image uses the root Viewer path and an empty API base. For ipproxy,
build the verified source revision with the ipproxy-only path arguments and
record the resulting image ID in the runtime `.env`:

```bash
release_sha=<verified-git-commit>
runtime_env=/home/ipproxy/vfs-link-ipproxy/.env
build_dir=/home/ipproxy/vfs-link-ipproxy/build/$release_sha
image_tag=vfs-link/file-server:ipproxy-$release_sha
vite_base_path=$(sed -n 's/^VITE_BASE_PATH=//p' "$runtime_env")
vite_api_base_url=$(sed -n 's/^VITE_API_BASE_URL=//p' "$runtime_env")

test "$vite_base_path" = /
test -z "$vite_api_base_url"

docker build \
  --build-arg VITE_BASE_PATH="$vite_base_path" \
  --build-arg VITE_API_BASE_URL="$vite_api_base_url" \
  --label "org.opencontainers.image.revision=$release_sha" \
  --label org.opencontainers.image.title=vfs-link-ipproxy \
  -f "$build_dir/apps/file-server/Dockerfile" \
  -t "$image_tag" \
  "$build_dir"
docker image inspect --format '{{.Id}}' "$image_tag"
```

Set `VFS_LINK_IMAGE` to that `sha256:...` image ID. Keep the source tree at
`build/$release_sha` so the deployed binary remains reproducible and auditable.

Before deployment, extract the generated web assets and verify both ipproxy
build-time values are present. This check is read-only for the image and can be
rerun for every candidate image:

```bash
container_id=$(docker create "$image_tag")
artifact_dir=$(mktemp -d)
trap 'docker rm -f "$container_id" >/dev/null 2>&1 || true; rm -rf "$artifact_dir"' EXIT
docker cp "$container_id:/app/web/." "$artifact_dir/"
docker rm "$container_id"

rg --fixed-strings 'src="/assets/' "$artifact_dir/index.html"
if rg --fixed-strings '/vfs-link' "$artifact_dir"; then
  echo 'legacy /vfs-link prefix remains in the ipproxy web artifact' >&2
  exit 1
fi
```

The static check confirms the root-path build reached the bundle. The
deployment acceptance check must still confirm in browser developer tools that
loading `http://vfs-link/` requests `/api/files` on the same hostname.

## Deploy

On ipproxy, create the persistent payload directory, copy this Compose file
into the runtime directory, and ensure the external Docker network already
exists. These commands create no database or object data; the database must
have been provisioned separately. Run them from
`/home/ipproxy/vfs-link-ipproxy`.

```bash
cd /home/ipproxy/vfs-link-ipproxy
install -d -m 0750 /home/ipproxy/vfs-link-ipproxy/data/objects /home/ipproxy/vfs-link-ipproxy/data/thumbnails
docker network inspect services >/dev/null

docker compose \
  --env-file /home/ipproxy/vfs-link-ipproxy/.env \
  -f docker-compose.ipproxy.yml config
docker compose \
  --env-file /home/ipproxy/vfs-link-ipproxy/.env \
  -f docker-compose.ipproxy.yml up -d
docker compose \
  --env-file /home/ipproxy/vfs-link-ipproxy/.env \
  -f docker-compose.ipproxy.yml ps
```

The profile exposes TCP `21`, the configured HTTP host port (default `18080`),
and the configured inclusive FTP passive range (default `30000-30100`). Ensure
the host firewall and any edge NAT publish the same ports, and advertise a
public hostname in `FTP_PASV_URL`; do not use an internal-only address for
remote FTP clients.

## Acceptance checks for the new empty instance

Run the following after `up -d`. Substitute the real HTTP origin and use
credentials where HTTP Basic Auth is enabled.

```bash
docker inspect --format '{{.State.Health.Status}}' vfs-link-ipproxy
curl --fail --user '<http-user>:<http-password>' \
  http://<http-origin>/api/status
docker compose \
  --env-file /home/ipproxy/vfs-link-ipproxy/.env \
  -f docker-compose.ipproxy.yml exec file-server \
  ./physical-health --fail-on-unhealthy
```

Then perform one end-to-end upload, thumbnail replacement, listing, download, and permanent-delete
through the intended client path (browser/API, FTP, and/or WebDAV). Confirm
that the deleted test payload disappears beneath
`/home/ipproxy/vfs-link-ipproxy/data/objects`, thumbnail WebP files are kept
under `/home/ipproxy/vfs-link-ipproxy/data/thumbnails`, and the active database
is still `vfs_link_ipproxy`. Do not run `physical-health` against the archive as
an acceptance check for this new empty deployment: its records intentionally
refer to discarded payloads.

For general configuration details, see [Configuration reference](configuration.md),
[Self-hosting](self-hosting.md), and [Networking and exposure](networking.md).
