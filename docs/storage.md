# Storage

Each logical path is mapped by the selected metadata database to a
`physicalHash` object key. Set `DB_DRIVER=postgres` for PostgreSQL or
`DB_DRIVER=json` for a versioned JSON snapshot stored on `STORAGE_DRIVER`.
The active storage backend holds the bytes; moving a logical path only updates
the database mapping.

## Local storage

The default Compose setup stores objects in the `objectdata` named volume and
the mapping table in `pgdata`. Back up and restore both together. A bind mount
may be substituted for `objectdata` when host-managed storage is preferred.

With `DB_DRIVER=json`, metadata is written beneath
`LOCAL_STORAGE_ROOT/_vfs-link/` using a temporary file, fsync, and atomic rename.
Local JSON coordinates writers inside one process and is not a shared-disk
multi-process database.

## Google Cloud Storage

Set `STORAGE_DRIVER=gcs`, `GCS_BUCKET`, and use `docker-compose.gcs.yml` to
mount an Application Default Credentials-compatible service account file. Grant
only the object read/write permissions needed for that bucket. Keep credentials
outside the repository and mount them read-only.

Changing `STORAGE_DRIVER` does not copy data between local storage and GCS.
Plan and verify a migration separately before changing production settings.

GCS is the supported primary store for stateless/serverless deployments. Local
storage is intended for development or a persistent single-instance host.
WebDAV range downloads map to range readers, while `PUT` streams into a new
object before the logical mapping is published. Standard WebDAV does not add a
cross-request resumable upload protocol.

With `DB_DRIVER=json`, metadata defaults to
`gs://$GCS_BUCKET/_vfs-link/metadata.json`. Every mutation reloads the snapshot
and writes it with a generation-match precondition. Conflicts retry with bounded
backoff. The reserved `_vfs-link/` prefix is excluded from user listings and
mapping rebuilds.

## Browser uploads

Both storage drivers expose the same create, upload, and complete contract.
Local uploads return a file-server `uploadUrl`; GCS uploads return an
authenticated resumable session URL so bytes travel directly from the browser
to Cloud Storage. Completion verifies the object size before atomically
publishing the logical mapping. The default maximum file size is 50 GiB.

## File sharing

Sharing reads from the active store and exports to `SHARE_GCS_BUCKET`. It is
independent from `GCS_BUCKET`. Configure public access, a signed-URL layer, or
another distribution method appropriate for your security requirements before
sharing links with users.

`PUB_SUB_DRIVER=goroutine` preserves the self-hosted background behavior.
`PUB_SUB_DRIVER=pubsub` publishes a small share job and processes it through an
authenticated Pub/Sub push endpoint. GCS-to-GCS shares use server-side copy so
large objects do not travel through the file-server container.
