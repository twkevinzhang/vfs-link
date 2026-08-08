#!/usr/bin/env bash
set -euo pipefail

ACCOUNT="${ACCOUNT:-twkevinzhang@gmail.com}"
FINOPS_PROJECT_ID="${FINOPS_PROJECT_ID:-chromatic-idea-405303}"
REGION="${REGION:-asia-east1}"
SERVICE="${SERVICE:-budget-kill-switch}"
TRIGGER="${TRIGGER:-budget-kill-trigger}"
CONFIRM_DISARM="${CONFIRM_DISARM:-}"

for command_name in gcloud jq; do
  command -v "$command_name" >/dev/null || {
    echo "Required command not found: $command_name" >&2
    exit 1
  }
done

if [[ "$CONFIRM_DISARM" != "$FINOPS_PROJECT_ID" ]]; then
  echo "Set CONFIRM_DISARM=${FINOPS_PROJECT_ID} to remove event delivery and force DRY_RUN=true." >&2
  exit 1
fi

# First make any already queued delivery harmless, while preserving the
# approved cost settings instead of inheriting a historical revision.
gcloud run services update "$SERVICE" --region="$REGION" --project="$FINOPS_PROJECT_ID" \
  --account="$ACCOUNT" --update-env-vars=DRY_RUN=true \
  --cpu=1 --memory=512Mi --cpu-throttling --concurrency=1 \
  --min=0 --min-instances=0 --max-instances=1 --timeout=60s --quiet

service_max="$(gcloud run services describe "$SERVICE" --region="$REGION" --project="$FINOPS_PROJECT_ID" \
  --account="$ACCOUNT" --format=json --quiet | jq -r '.metadata.annotations["run.googleapis.com/maxScale"] // ""')"
if [[ "$service_max" != '1' ]]; then
  echo 'Refusing to continue: service-level max instances is not 1.' >&2
  exit 1
fi

if gcloud eventarc triggers describe "$TRIGGER" --location="$REGION" --project="$FINOPS_PROJECT_ID" \
  --account="$ACCOUNT" --quiet >/dev/null 2>&1; then
  gcloud eventarc triggers delete "$TRIGGER" --location="$REGION" --project="$FINOPS_PROJECT_ID" \
    --account="$ACCOUNT" --quiet
fi

echo 'Disarmed: DRY_RUN=true and Eventarc trigger removed. Budget/topic/function remain for inspection.'
