# Cloud Run HTTP file-server

The serverless deployment runs only the browser and HTTP API. FTP and WebDAV are
disabled. Primary file bytes, thumbnail bytes, and JSON metadata live in
separate Cloud Storage buckets.

```dotenv
FTP_ENABLED=false
WEBDAV_ENABLED=false
STORAGE_DRIVER=gcs
GCS_BUCKET=your-vfs-link-production-bucket
THUMBNAIL_STORAGE_DRIVER=gcs
THUMBNAIL_GCS_BUCKET=your-vfs-link-thumbnail-bucket
DB_DRIVER=json
METADATA_STORAGE_DRIVER=gcs
METADATA_GCS_BUCKET=your-vfs-link-metadata-bucket
METADATA_PREFIX=_vfs-link-v3
METADATA_SHARD_COUNT=64
METADATA_REDUCER_INTERVAL=2s
METADATA_MUTATION_MODE=global
HTTP_BASIC_AUTH_ENABLED=true
PUB_SUB_DRIVER=pubsub
GCP_PROJECT_ID=your-project-id
PUB_SUB_TOPIC=vfs-link-share-jobs
```

Use dedicated private Standard-class thumbnail and metadata buckets in the same
region as Cloud Run. The primary bucket may use Archive storage for
low-frequency file bytes. Grant the runtime service account object access to
the primary, thumbnail, metadata, and share buckets plus publisher access to
the share-job topic. Enforce uniform bucket-level access and public-access
prevention on the thumbnail bucket; thumbnail reads stay behind the application
API. Store the HTTP
password and Telegram bot token in Secret Manager.

Google Cloud does not allow IAM Conditions on `allUsers` public bindings. The
deployment helper therefore grants anonymous object-view access to the
dedicated share bucket as a whole. Never place private objects in this bucket;
the primary object bucket remains private. Remove that binding and replace
public URLs with a signed-URL layer if shares must be revocable or private.

Pub/Sub uses an authenticated push subscription targeting
`/internal/pubsub/shares`. The endpoint validates the Google-signed OIDC token,
audience, verified email, and configured push service account. It is mounted
outside application Basic Auth. Delivery is at least once; the metadata store
uses an atomic lease and completed jobs are idempotent.

JSON tree moves, trash, restore, and permanent deletion return an operation ID
and continue from a persisted manifest after the initiating response. Deploy
Cloud Run with instance-based billing (`--no-cpu-throttling`) so CPU remains
available for that background work after a `202 Accepted` response. Minimum
instances can remain zero, so the service still scales to zero; while an
instance exists, instance-based CPU and memory billing applies.

Configure the primary bucket CORS policy to allow `PUT` from the exact Cloud Run
origin, allow the `Content-Type` and `Content-Range` request headers, and expose
the `Range` response header. Resumable clients need `Range` to reconcile the
storage-confirmed offset after a pause or ambiguous response. Do not use `*` in
production. GCS-to-GCS shares use Cloud Storage's server-side copy operation. If
Telegram credentials are absent, the copy can complete but notification status
becomes `notification_failed`.

For a v4 cutover, migrate and validate `_vfs-link-v4` first, then deploy with
`METADATA_PREFIX=_vfs-link-v4` and `METADATA_MUTATION_MODE=scoped`. Do not
change only the prefix on an active revision; an empty or partially imported
prefix is not a valid production metadata source.

The Cloud Run filesystem is not persistent. Never select local file or metadata
storage for the production service; local remains supported for development
and persistent single-instance self-hosting.

For a v2-to-v3 prefix rollout, keep `_vfs-link-v2` unchanged, migrate into
`_vfs-link-v3`, and deploy the v3 configuration as a no-traffic revision before
cutting over. See [metadata-migration.md](metadata-migration.md) for the
maintenance, validation, and rollback sequence.
