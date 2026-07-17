# Cloud Run HTTP file-server

The serverless deployment runs only the browser and HTTP API. FTP and WebDAV are
disabled. File bytes and JSON metadata live in separate Cloud Storage buckets.

```dotenv
FTP_ENABLED=false
WEBDAV_ENABLED=false
STORAGE_DRIVER=gcs
GCS_BUCKET=your-vfs-link-production-bucket
DB_DRIVER=json
METADATA_STORAGE_DRIVER=gcs
METADATA_GCS_BUCKET=your-vfs-link-metadata-bucket
METADATA_PREFIX=_vfs-link
HTTP_BASIC_AUTH_ENABLED=true
PUB_SUB_DRIVER=pubsub
GCP_PROJECT_ID=your-project-id
PUB_SUB_TOPIC=vfs-link-share-jobs
```

Use a dedicated Standard-class metadata bucket in the same region as Cloud Run.
The primary bucket may use Archive storage for low-frequency file bytes. Grant
the runtime service account object access to the primary, metadata, and share
buckets plus publisher access to the share-job topic. Store the HTTP
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
origin. Do not use `*` in production. GCS-to-GCS shares use Cloud Storage's
server-side copy operation. If Telegram credentials are absent, the copy can
complete but notification status becomes `notification_failed`.

The Cloud Run filesystem is not persistent. Never select local file or metadata
storage for the production service; local remains supported for development
and persistent single-instance self-hosting.
