# Self-hosting

This guide deploys one vfs-link instance with PostgreSQL and local object
storage. It is suitable for a private server or trusted internal network.

## Prerequisites

- Docker Engine and Docker Compose v2.
- An HTTPS reverse proxy in front of TCP `8080` for WebDAV and the browser API.
- During FTP migration only, firewall rules for TCP `21` and the selected
  passive range (`30000-30100` by default).

## Local storage deployment

```bash
git clone https://github.com/twkevinzhang/vfs-link.git
cd vfs-link
cp .env.example .env
```

Edit `.env` before starting. Replace every `CHANGE_ME` password.
Set `FTP_PASV_URL` to the public DNS name or IP when remote FTP clients connect;
`127.0.0.1` only works for clients on the same host. The bundled Compose file
does not expose PostgreSQL to the host.

```bash
docker compose config
docker compose up -d --build
docker compose ps
curl -fsS http://localhost:8080/api/status
```

After configuring HTTPS, use a WebDAV client with `WEBDAV_USER` and
`WEBDAV_PASS` at `https://your-host.example/dav/`. Upload a small test file,
list it, download it, then check the browser API. FTP remains available while
`FTP_ENABLED=true`; disable it after client migration.

## GCS deployment

GCS is optional. It can be the primary object store (`STORAGE_DRIVER=gcs`) or
the destination for file sharing. Create a credential with only the bucket
permissions required by the chosen mode, store it outside this repository, and
set its absolute host path in `GOOGLE_APPLICATION_CREDENTIALS_HOST`.

Set `GCS_BUCKET` when using GCS as primary storage. Set `SHARE_GCS_BUCKET` for
sharing. Then start with the credential overlay:

```bash
docker compose -f docker-compose.yml -f docker-compose.gcs.yml up -d --build
```

The credential is mounted read-only at runtime and is not copied into the image.
Never commit it, place it in a Docker image, or paste it into an issue.

## Upgrades and removal

```bash
git pull --ff-only
docker compose up -d --build
docker compose logs --tail=100 file-server
```

`docker compose down` stops the stack while retaining data volumes. `docker
compose down -v` permanently deletes the PostgreSQL and local object volumes;
make a backup first.

See [operations.md](operations.md) for backup and restore procedures.
