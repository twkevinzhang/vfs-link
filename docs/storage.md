# Storage

Each logical path is mapped by the selected metadata database to a
`physicalHash` object key. Set `DB_DRIVER=postgres` for PostgreSQL or
`DB_DRIVER=json` for a tree of small JSON metadata objects stored on the
independent `METADATA_STORAGE_DRIVER`.
The active storage backend holds the bytes; moving a logical path only updates
the database mapping.

## Local storage

The default Compose setup stores objects in the `objectdata` named volume and
the mapping table in `pgdata`. Back up and restore both together. A bind mount
may be substituted for `objectdata` when host-managed storage is preferred.

With `DB_DRIVER=json`, metadata is written beneath
`METADATA_LOCAL_ROOT/_vfs-link/` using temporary files, fsync, and atomic rename.
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

With `DB_DRIVER=json`, configure `METADATA_STORAGE_DRIVER=gcs` and a dedicated
`METADATA_GCS_BUCKET`. The tree stores one sidecar per file and directory,
directory indexes, entity records, operation manifests, and aggregate stats
beneath a versioned reserved prefix. Tree v2 under `_vfs-link-v2/` adds
persisted subtree folder summaries. Tree v3 under `_vfs-link-v3/` uses relative
NFC logical paths. Tree v4 under `_vfs-link-v4/` uses 64 name-hash shards per
directory by default, scoped leases, transaction manifests, and immutable
derived-metadata deltas. File paths remain strongly consistent; folder
summaries and global statistics converge asynchronously. Writes use
generation-match preconditions. The metadata bucket should use Standard storage
in the same region as Cloud Run; an Archive-class primary bucket should hold
file bytes, not frequently read or rewritten metadata.

The legacy single `_vfs-link/metadata.json` snapshot is not read by the runtime.
It can be kept as an offline migration/rollback backup only.

During a prefix migration, keep the source prefix immutable and intact. Switch
`METADATA_PREFIX` only after validating the new prefix. After v4 has accepted
writes, first fall back to v4 `global` mutation mode; do not switch directly to
the stale v3 snapshot without a validated reverse journal. See
[metadata-v4-cutover.md](metadata-v4-cutover.md).

## Thumbnail storage

Thumbnails are derived WebP objects and never share the primary file-byte
store. Configure `THUMBNAIL_STORAGE_DRIVER=local` with a separate
`THUMBNAIL_LOCAL_ROOT` for persistent self-hosting, or
`THUMBNAIL_STORAGE_DRIVER=gcs` with a dedicated `THUMBNAIL_GCS_BUCKET` for
Cloud Run. The GCS bucket must differ from `GCS_BUCKET`; configuration rejects
an equal name.

The Cloud Run deployment helper creates the thumbnail bucket as Standard class
in the service region, with uniform bucket-level access and public-access
prevention. Keep it private and serve thumbnail reads only through the
authenticated `/api/thumbnails/{id}` route. Grant the runtime service account
object access to this bucket, not anonymous access.

## Browser uploads

Both storage drivers expose the same create, upload, and complete contract.
Local uploads return a file-server `uploadUrl`; GCS uploads return an
authenticated resumable session URL so bytes travel directly from the browser
to Cloud Storage. The upload writes once to the sanitized final key; completion
verifies the object size before atomically publishing the logical mapping. No
post-upload copy or move is performed. The default maximum file size is 50 GiB.

Browser, VFS/FTP, and WebDAV uploads share the same deterministic key rule.
Unsupported portable filename characters become `_`, while directory separators
remain intact. Logical rename and move operations intentionally leave that key
unchanged. Use the optional [drift viewer](drift.md) when an operator explicitly
wants to reconcile physical keys with the current logical index.

## File sharing

Sharing reads from the active store and exports to `SHARE_GCS_BUCKET`. It is
independent from `GCS_BUCKET`. Configure public access, a signed-URL layer, or
another distribution method appropriate for your security requirements before
sharing links with users.

`PUB_SUB_DRIVER=goroutine` preserves the self-hosted background behavior.
`PUB_SUB_DRIVER=pubsub` publishes a small share job and processes it through an
authenticated Pub/Sub push endpoint. GCS-to-GCS shares use server-side copy so
large objects do not travel through the file-server container.
