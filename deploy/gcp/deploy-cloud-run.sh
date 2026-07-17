#!/usr/bin/env bash
set -euo pipefail

: "${PROJECT_ID:?Set PROJECT_ID}"
: "${HTTP_BASIC_AUTH_PASS:?Set HTTP_BASIC_AUTH_PASS}"

REGION="${REGION:-asia-east1}"
SERVICE="${SERVICE:-vfs-link-file-server}"
REPOSITORY="${REPOSITORY:-vfs-link}"
PRIMARY_BUCKET="${PRIMARY_BUCKET:-${PROJECT_ID}-archive}"
PRIMARY_BUCKET_LOCATION="${PRIMARY_BUCKET_LOCATION:-us-east5}"
PRIMARY_BUCKET_CLASS="${PRIMARY_BUCKET_CLASS:-ARCHIVE}"
METADATA_BUCKET="${METADATA_BUCKET:-${PROJECT_ID}-vfs-link-metadata}"
METADATA_PREFIX="${METADATA_PREFIX:-_vfs-link}"
SHARE_BUCKET="${SHARE_BUCKET:-${PROJECT_ID}-vfs-link-shares}"
TOPIC="${TOPIC:-vfs-link-share-jobs}"
DEAD_LETTER_TOPIC="${DEAD_LETTER_TOPIC:-vfs-link-share-dead-letter}"
SUBSCRIPTION="${SUBSCRIPTION:-vfs-link-share-worker}"
RUNTIME_SA_NAME="${RUNTIME_SA_NAME:-vfs-link-runtime}"
PUSH_SA_NAME="${PUSH_SA_NAME:-vfs-link-pubsub-push}"
HTTP_BASIC_AUTH_USER="${HTTP_BASIC_AUTH_USER:-vfs_link}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-}"
MAINTENANCE_MODE="${MAINTENANCE_MODE:-false}"

retry() {
  local attempt
  for attempt in {1..12}; do
    if "$@"; then
      return 0
    fi
    if [[ "$attempt" -eq 12 ]]; then
      return 1
    fi
    sleep 5
  done
}

PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
RUNTIME_SA="${RUNTIME_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
PUSH_SA="${PUSH_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
PUBSUB_AGENT="service-${PROJECT_NUMBER}@gcp-sa-pubsub.iam.gserviceaccount.com"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPOSITORY}/file-server:manual-$(date -u +%Y%m%d%H%M%S)"

gcloud services enable \
  run.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com \
  pubsub.googleapis.com secretmanager.googleapis.com iamcredentials.googleapis.com \
  --project="$PROJECT_ID"

if ! gcloud artifacts repositories describe "$REPOSITORY" --location="$REGION" --project="$PROJECT_ID" >/dev/null 2>&1; then
  gcloud artifacts repositories create "$REPOSITORY" --repository-format=docker --location="$REGION" --project="$PROJECT_ID"
fi

if ! gcloud storage buckets describe "gs://${PRIMARY_BUCKET}" --project="$PROJECT_ID" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://${PRIMARY_BUCKET}" --location="$PRIMARY_BUCKET_LOCATION" \
    --default-storage-class="$PRIMARY_BUCKET_CLASS" --uniform-bucket-level-access --project="$PROJECT_ID"
fi
for bucket in "$METADATA_BUCKET" "$SHARE_BUCKET"; do
  if ! gcloud storage buckets describe "gs://${bucket}" --project="$PROJECT_ID" >/dev/null 2>&1; then
    gcloud storage buckets create "gs://${bucket}" --location="$REGION" \
      --default-storage-class=STANDARD --uniform-bucket-level-access --project="$PROJECT_ID"
  fi
done
gcloud storage buckets add-iam-policy-binding "gs://${SHARE_BUCKET}" \
  --member=allUsers --role=roles/storage.objectViewer --project="$PROJECT_ID" >/dev/null

for account in "$RUNTIME_SA_NAME" "$PUSH_SA_NAME"; do
  if ! gcloud iam service-accounts describe "${account}@${PROJECT_ID}.iam.gserviceaccount.com" --project="$PROJECT_ID" >/dev/null 2>&1; then
    gcloud iam service-accounts create "$account" --project="$PROJECT_ID"
  fi
done

for bucket in "$PRIMARY_BUCKET" "$METADATA_BUCKET" "$SHARE_BUCKET"; do
  retry gcloud storage buckets add-iam-policy-binding "gs://${bucket}" \
    --member="serviceAccount:${RUNTIME_SA}" --role=roles/storage.objectAdmin --project="$PROJECT_ID" >/dev/null
done

for topic in "$TOPIC" "$DEAD_LETTER_TOPIC"; do
  gcloud pubsub topics describe "$topic" --project="$PROJECT_ID" >/dev/null 2>&1 || \
    gcloud pubsub topics create "$topic" --project="$PROJECT_ID"
done
gcloud pubsub topics add-iam-policy-binding "$TOPIC" --project="$PROJECT_ID" \
  --member="serviceAccount:${RUNTIME_SA}" --role=roles/pubsub.publisher >/dev/null
gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="serviceAccount:${PUBSUB_AGENT}" \
  --role=roles/pubsub.subscriber >/dev/null
gcloud pubsub topics add-iam-policy-binding "$DEAD_LETTER_TOPIC" --project="$PROJECT_ID" \
  --member="serviceAccount:${PUBSUB_AGENT}" --role=roles/pubsub.publisher >/dev/null

if ! gcloud secrets describe vfs-link-http-basic-password --project="$PROJECT_ID" >/dev/null 2>&1; then
  gcloud secrets create vfs-link-http-basic-password --replication-policy=automatic --project="$PROJECT_ID"
fi
printf '%s' "$HTTP_BASIC_AUTH_PASS" | gcloud secrets versions add vfs-link-http-basic-password --data-file=- --project="$PROJECT_ID" >/dev/null
gcloud secrets add-iam-policy-binding vfs-link-http-basic-password --project="$PROJECT_ID" \
  --member="serviceAccount:${RUNTIME_SA}" --role=roles/secretmanager.secretAccessor >/dev/null

SECRET_ARGS="HTTP_BASIC_AUTH_PASS=vfs-link-http-basic-password:latest"
if [[ -n "${TELEGRAM_BOT_TOKEN:-}" ]]; then
  if ! gcloud secrets describe vfs-link-telegram-bot-token --project="$PROJECT_ID" >/dev/null 2>&1; then
    gcloud secrets create vfs-link-telegram-bot-token --replication-policy=automatic --project="$PROJECT_ID"
  fi
  printf '%s' "$TELEGRAM_BOT_TOKEN" | gcloud secrets versions add vfs-link-telegram-bot-token --data-file=- --project="$PROJECT_ID" >/dev/null
  gcloud secrets add-iam-policy-binding vfs-link-telegram-bot-token --project="$PROJECT_ID" \
    --member="serviceAccount:${RUNTIME_SA}" --role=roles/secretmanager.secretAccessor >/dev/null
  SECRET_ARGS="${SECRET_ARGS},TELEGRAM_BOT_TOKEN=vfs-link-telegram-bot-token:latest"
fi

gcloud builds submit --config=deploy/gcp/cloudbuild.yaml \
  --substitutions="_IMAGE=${IMAGE}" --project="$PROJECT_ID" .

COMMON_ENV="FTP_ENABLED=false,WEBDAV_ENABLED=false,STORAGE_DRIVER=gcs,GCS_BUCKET=${PRIMARY_BUCKET},DB_DRIVER=json,METADATA_STORAGE_DRIVER=gcs,METADATA_GCS_BUCKET=${METADATA_BUCKET},METADATA_PREFIX=${METADATA_PREFIX},MAINTENANCE_MODE=${MAINTENANCE_MODE},HTTP_BASIC_AUTH_ENABLED=true,HTTP_BASIC_AUTH_USER=${HTTP_BASIC_AUTH_USER},HTTP_CORS_ORIGINS=,UPLOAD_MAX_BYTES=53687091200,UPLOAD_SESSION_TTL=24h,SHARE_GCS_BUCKET=${SHARE_BUCKET},SHARE_GCS_PREFIX=shares,SHARE_PUBLIC_BASE_URL=https://storage.googleapis.com/${SHARE_BUCKET},TELEGRAM_CHAT_ID=${TELEGRAM_CHAT_ID},GCP_PROJECT_ID=${PROJECT_ID},PUB_SUB_TOPIC=${TOPIC}"

gcloud run deploy "$SERVICE" --image="$IMAGE" --region="$REGION" --project="$PROJECT_ID" \
  --service-account="$RUNTIME_SA" --allow-unauthenticated --port=8080 \
  --memory=1Gi --cpu=1 --no-cpu-throttling --concurrency=8 --max-instances=10 --timeout=3600 \
  --set-env-vars="${COMMON_ENV},PUB_SUB_DRIVER=goroutine" --set-secrets="$SECRET_ARGS"

SERVICE_URL="$(gcloud run services describe "$SERVICE" --region="$REGION" --project="$PROJECT_ID" --format='value(status.url)')"
gcloud run services add-iam-policy-binding "$SERVICE" --region="$REGION" --project="$PROJECT_ID" \
  --member="serviceAccount:${PUSH_SA}" --role=roles/run.invoker >/dev/null
gcloud iam service-accounts add-iam-policy-binding "$PUSH_SA" --project="$PROJECT_ID" \
  --member="serviceAccount:${PUBSUB_AGENT}" --role=roles/iam.serviceAccountTokenCreator >/dev/null

if gcloud pubsub subscriptions describe "$SUBSCRIPTION" --project="$PROJECT_ID" >/dev/null 2>&1; then
  gcloud pubsub subscriptions update "$SUBSCRIPTION" --project="$PROJECT_ID" \
    --push-endpoint="${SERVICE_URL}/internal/pubsub/shares" \
    --push-auth-service-account="$PUSH_SA" --push-auth-token-audience="$SERVICE_URL" \
    --dead-letter-topic="$DEAD_LETTER_TOPIC" --max-delivery-attempts=10 \
    --min-retry-delay=10s --max-retry-delay=600s
else
  gcloud pubsub subscriptions create "$SUBSCRIPTION" --topic="$TOPIC" --project="$PROJECT_ID" \
    --push-endpoint="${SERVICE_URL}/internal/pubsub/shares" \
    --push-auth-service-account="$PUSH_SA" --push-auth-token-audience="$SERVICE_URL" \
    --dead-letter-topic="$DEAD_LETTER_TOPIC" --max-delivery-attempts=10 \
    --min-retry-delay=10s --max-retry-delay=600s
fi

gcloud run services update "$SERVICE" --region="$REGION" --project="$PROJECT_ID" \
  --update-env-vars="PUB_SUB_DRIVER=pubsub,PUB_SUB_PUSH_AUDIENCE=${SERVICE_URL},PUB_SUB_PUSH_SERVICE_ACCOUNT=${PUSH_SA}"

CORS_FILE="$(mktemp)"
trap 'rm -f "$CORS_FILE"' EXIT
printf '[{"origin":["%s"],"method":["PUT","POST","GET","HEAD","OPTIONS"],"responseHeader":["Content-Type","Range"],"maxAgeSeconds":3600}]\n' "$SERVICE_URL" >"$CORS_FILE"
gcloud storage buckets update "gs://${PRIMARY_BUCKET}" --cors-file="$CORS_FILE" --project="$PROJECT_ID"

printf 'Cloud Run URL: %s\n' "$SERVICE_URL"
printf 'HTTP user: %s\n' "$HTTP_BASIC_AUTH_USER"
printf 'Primary object bucket: gs://%s (%s)\n' "$PRIMARY_BUCKET" "$PRIMARY_BUCKET_CLASS"
printf 'Metadata bucket: gs://%s (STANDARD, %s)\n' "$METADATA_BUCKET" "$REGION"
printf 'Retrieve the password with: gcloud secrets versions access latest --secret=vfs-link-http-basic-password --project=%s\n' "$PROJECT_ID"
