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

## Maintenance tools

The image includes two tools:

```bash
# Rebuild mappings from objects in the active store. Review the warning first.
docker compose exec file-server ./file-server rebuild-mapping --yes

# Compare database mappings with objects without changing data.
docker compose exec file-server ./physical-health --fail-on-unhealthy
```

`rebuild-mapping` is destructive to existing mapping assumptions; use it only
with a verified backup and an object-key layout it understands. `physical-health`
is read-only and is the preferred first diagnostic step.
