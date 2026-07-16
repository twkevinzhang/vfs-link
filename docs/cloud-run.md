# Cloud Run HTTP file-server

The serverless deployment runs only the browser and HTTP API. FTP and WebDAV are
disabled. File bytes and JSON metadata both live in Cloud Storage.

```dotenv
FTP_ENABLED=false
WEBDAV_ENABLED=false
STORAGE_DRIVER=gcs
GCS_BUCKET=your-vfs-link-production-bucket
DB_DRIVER=json
JSON_DB_OBJECT=_vfs-link/metadata.json
HTTP_BASIC_AUTH_ENABLED=true
PUB_SUB_DRIVER=pubsub
GCP_PROJECT_ID=your-project-id
PUB_SUB_TOPIC=vfs-link-share-jobs
```

Use a dedicated runtime service account with object access to the primary and
share buckets plus publisher access to the share-job topic. Store the HTTP
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

Configure the primary bucket CORS policy to allow `PUT` from the exact Cloud Run
origin. Do not use `*` in production. GCS-to-GCS shares use Cloud Storage's
server-side copy operation. If Telegram credentials are absent, the copy can
complete but notification status becomes `notification_failed`.

The Cloud Run filesystem is not persistent. Never select local storage for the
production service; local remains supported for development and persistent
single-instance self-hosting.
