# Operations

## Status and logs

```bash
docker compose ps
docker compose logs --tail=100 file-server db
curl -fsS http://localhost:8080/api/status
```

## Backup and restore

Back up the PostgreSQL mapping and object storage as one consistent unit. For a
small maintenance window, stop WebDAV and FTP writes before taking the backup.

```bash
set -a; . ./.env; set +a
docker compose exec -T db pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB" > vfs-link.sql
docker run --rm -v vfs-link_objectdata:/data -v "$PWD":/backup alpine \
  tar czf /backup/vfs-link-objects.tar.gz -C /data .
```

Restore the database before bringing the file server online, then restore the
matching object volume. Adjust the volume name if you use a custom Compose
project name.

For `DB_DRIVER=json`, the metadata tree lives under the versioned prefix named
by `METADATA_PREFIX` (for example `_vfs-link-v2/` or `_vfs-link-v3/`). Back up the metadata bucket consistently
with file objects. On versioned GCS buckets, retain noncurrent metadata
generations long enough to recover from an accidental logical mutation. The
old monolithic `metadata.json` is only an offline backup and is not a runtime
fallback.

## Move and trash API

The browser uses the same batch endpoints for JSON and PostgreSQL metadata.
PostgreSQL completes the metadata transaction synchronously. The JSON tree
backend returns `202 Accepted` with an `operationId`; poll
`GET /api/operations/{operationId}` until it is completed. Tree operations use
persisted manifests, leases, checkpoints, and retries because Cloud Storage
cannot rename a directory tree atomically.

| Endpoint | Body | Behavior |
| --- | --- | --- |
| `POST /api/files/move` | `{"paths":["/a"],"destination":"/archive"}` | Moves selected roots; PostgreSQL is atomic, while JSON tree executes a recoverable operation. |
| `POST /api/files/trash` | `{"paths":["/a"]}` | Hides active mappings while retaining physical objects. |
| `GET /api/trash` | none | Lists top-level trash groups. |
| `POST /api/trash/restore` | `{"trashIds":["..."]}` | Restores whole groups atomically; active-path conflicts reject the batch. |
| `POST /api/trash/delete` | `{"trashIds":["..."]}` | Permanently deletes the selected physical objects and metadata. |
| `POST /api/trash/empty` | `{}` | Permanently deletes every trash group. |

Moving an item to trash does not move or copy its bytes. Storage usage drops
only after permanent deletion. A permanent-delete claim prevents another
Cloud Run instance from restoring a mapping while its local or GCS object is
being deleted; a failed object deletion can be retried safely.

For JSON tree metadata, a single-file operation normally finishes quickly;
large directory operations are eventually consistent while their operation is
running. The UI reports progress and refreshes the affected listing after the
completion status is observed.

## Maintenance tools

The image includes three tools:

```bash
# Rebuild mappings from objects in the active store. Review the warning first.
docker compose exec file-server ./file-server rebuild-mapping --yes

# Compare database mappings with objects without changing data.
docker compose exec file-server ./physical-health --fail-on-unhealthy

# Also compare active records, stats.json, and the root folder summary.
docker compose exec file-server ./physical-health \
  --check-metadata-aggregates --fail-on-unhealthy

# Distributed tree prefix migration (or historical metadata.json import).
docker compose exec file-server ./metadata-migrate --help
```

`rebuild-mapping` is destructive to existing mapping assumptions; use it only
with a verified backup and an object-key layout it understands. `physical-health`
is read-only and is the preferred first diagnostic step.

Unreferenced thumbnails remain recoverable for seven days; the server then
scans and removes them outside the request path from the dedicated thumbnail
store. Back up that store alongside thumbnail metadata while the retention
window matters.
