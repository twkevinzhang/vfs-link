# Storage

Each logical FTP path is mapped in PostgreSQL to a `physicalHash` object key.
The active storage backend holds the bytes; moving a logical path only updates
the database mapping.

## Local storage

The default Compose setup stores objects in the `objectdata` named volume and
the mapping table in `pgdata`. Back up and restore both together. A bind mount
may be substituted for `objectdata` when host-managed storage is preferred.

## Google Cloud Storage

Set `STORAGE_DRIVER=gcs`, `GCS_BUCKET`, and use `docker-compose.gcs.yml` to
mount an Application Default Credentials-compatible service account file. Grant
only the object read/write permissions needed for that bucket. Keep credentials
outside the repository and mount them read-only.

Changing `STORAGE_DRIVER` does not copy data between local storage and GCS.
Plan and verify a migration separately before changing production settings.

## File sharing

Sharing reads from the active store and exports to `SHARE_GCS_BUCKET`. It is
independent from `GCS_BUCKET`. Configure public access, a signed-URL layer, or
another distribution method appropriate for your security requirements before
sharing links with users.
