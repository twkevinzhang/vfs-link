#!/usr/bin/env bash
set -euo pipefail

ACCOUNT="${ACCOUNT:-twkevinzhang@gmail.com}"
TARGET_PROJECT_ID="${TARGET_PROJECT_ID:-storage-403503}"
TARGET_PROJECT_NUMBER="${TARGET_PROJECT_NUMBER:-692964716639}"
FINOPS_PROJECT_ID="${FINOPS_PROJECT_ID:-chromatic-idea-405303}"
BILLING_ACCOUNT_ID="${BILLING_ACCOUNT_ID:-01420A-B7869F-A617B2}"
REGION="${REGION:-asia-east1}"
SERVICE="${SERVICE:-budget-kill-switch}"
TOPIC="${TOPIC:-budget-kill-events}"
DLQ_TOPIC="${DLQ_TOPIC:-budget-kill-dead-letter}"
DLQ_SUBSCRIPTION="${DLQ_SUBSCRIPTION:-budget-kill-dead-letter-inspect}"
TRIGGER="${TRIGGER:-budget-kill-trigger}"
BUDGET_DISPLAY_NAME="${BUDGET_DISPLAY_NAME:-storage-403503-monthly-kill-switch-twd-1000}"
RUNTIME_SA_NAME="${RUNTIME_SA_NAME:-budget-kill-runtime}"
TRIGGER_SA_NAME="${TRIGGER_SA_NAME:-budget-kill-trigger}"
BUILD_SA_NAME="${BUILD_SA_NAME:-budget-kill-build}"
DRY_RUN="${DRY_RUN:-true}"
PREFLIGHT_FILE="${PREFLIGHT_FILE:-}"
CONFIRM_DEPLOY="${CONFIRM_DEPLOY:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_DIR="${SCRIPT_DIR}/function"

if [[ "$DRY_RUN" != 'true' ]]; then
  echo 'This deployment script intentionally refuses DRY_RUN=false. Production arming needs separate destructive approval.' >&2
  exit 1
fi
if [[ "$CONFIRM_DEPLOY" != "${FINOPS_PROJECT_ID}:DRY_RUN" ]]; then
  echo "Set CONFIRM_DEPLOY=${FINOPS_PROJECT_ID}:DRY_RUN after reviewing the saved preflight record." >&2
  exit 1
fi
if [[ -z "$PREFLIGHT_FILE" || ! -f "$PREFLIGHT_FILE" ]]; then
  echo 'PREFLIGHT_FILE must point to the saved preflight record.' >&2
  exit 1
fi
grep -Fq 'Deployment mode approved: DRY_RUN=true' "$PREFLIGHT_FILE"
grep -Fq 'min=0, max=1' "$PREFLIGHT_FILE"

for command_name in gcloud jq python3 npm curl; do
  command -v "$command_name" >/dev/null || {
    echo "Required command not found: $command_name" >&2
    exit 1
  }
done

gcloud_args=(--account="$ACCOUNT" --quiet)
runtime_sa="${RUNTIME_SA_NAME}@${FINOPS_PROJECT_ID}.iam.gserviceaccount.com"
trigger_sa="${TRIGGER_SA_NAME}@${FINOPS_PROJECT_ID}.iam.gserviceaccount.com"
build_sa="${BUILD_SA_NAME}@${FINOPS_PROJECT_ID}.iam.gserviceaccount.com"
topic_name="projects/${FINOPS_PROJECT_ID}/topics/${TOPIC}"
dlq_topic_name="projects/${FINOPS_PROJECT_ID}/topics/${DLQ_TOPIC}"

run_with_timeout() {
  local seconds="$1"
  shift
  python3 - "$seconds" "$@" <<'PY'
import subprocess
import sys

try:
    result = subprocess.run(sys.argv[2:], timeout=int(sys.argv[1]))
except subprocess.TimeoutExpired:
    print(f"Command exceeded {sys.argv[1]} seconds", file=sys.stderr)
    raise SystemExit(124)
raise SystemExit(result.returncode)
PY
}

ensure_service_account() {
  local name="$1" display_name="$2"
  if ! gcloud iam service-accounts describe "${name}@${FINOPS_PROJECT_ID}.iam.gserviceaccount.com" \
    --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}" >/dev/null 2>&1; then
    gcloud iam service-accounts create "$name" --display-name="$display_name" \
      --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}"
  fi
}

ensure_project_binding() {
  local project_id="$1" member="$2" role="$3"
  gcloud projects add-iam-policy-binding "$project_id" --member="$member" --role="$role" \
    --condition=None "${gcloud_args[@]}" >/dev/null
}

echo 'Verifying local package before any cloud resource creation...'
(cd "$SOURCE_DIR" && npm ci --ignore-scripts && npm test)

target_billing="$(gcloud billing projects describe "$TARGET_PROJECT_ID" "${gcloud_args[@]}" --format=json)"
finops_billing="$(gcloud billing projects describe "$FINOPS_PROJECT_ID" "${gcloud_args[@]}" --format=json)"
expected_account="billingAccounts/${BILLING_ACCOUNT_ID}"
jq -e --arg account "$expected_account" '.billingEnabled == true and .billingAccountName == $account' <<<"$target_billing" >/dev/null
jq -e --arg account "$expected_account" '.billingEnabled == true and .billingAccountName == $account' <<<"$finops_billing" >/dev/null

echo 'Enabling the minimal control-plane APIs in the FinOps project...'
run_with_timeout 600 gcloud services enable \
  billingbudgets.googleapis.com cloudbilling.googleapis.com run.googleapis.com \
  eventarc.googleapis.com pubsub.googleapis.com artifactregistry.googleapis.com \
  cloudbuild.googleapis.com iam.googleapis.com iamcredentials.googleapis.com \
  --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}"

ensure_service_account "$RUNTIME_SA_NAME" 'Budget kill switch runtime'
ensure_service_account "$TRIGGER_SA_NAME" 'Budget kill switch Eventarc trigger'
ensure_service_account "$BUILD_SA_NAME" 'Budget kill switch source builder'

ensure_project_binding "$TARGET_PROJECT_ID" "serviceAccount:${runtime_sa}" roles/billing.projectManager
ensure_project_binding "$TARGET_PROJECT_ID" "serviceAccount:${runtime_sa}" roles/browser
ensure_project_binding "$FINOPS_PROJECT_ID" "serviceAccount:${runtime_sa}" roles/logging.logWriter
ensure_project_binding "$FINOPS_PROJECT_ID" "serviceAccount:${trigger_sa}" roles/eventarc.eventReceiver
ensure_project_binding "$FINOPS_PROJECT_ID" "serviceAccount:${build_sa}" roles/run.builder

if ! gcloud pubsub topics describe "$TOPIC" --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}" >/dev/null 2>&1; then
  gcloud pubsub topics create "$TOPIC" --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}"
fi
if ! gcloud pubsub topics describe "$DLQ_TOPIC" --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}" >/dev/null 2>&1; then
  gcloud pubsub topics create "$DLQ_TOPIC" --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}"
fi
if ! gcloud pubsub subscriptions describe "$DLQ_SUBSCRIPTION" --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}" >/dev/null 2>&1; then
  gcloud pubsub subscriptions create "$DLQ_SUBSCRIPTION" --topic="$DLQ_TOPIC" \
    --message-retention-duration=7d --expiration-period=never \
    --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}"
fi

budgets_json="$(gcloud billing budgets list --billing-account="$BILLING_ACCOUNT_ID" \
  --billing-project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}" --format=json)"
matching_count="$(jq --arg name "$BUDGET_DISPLAY_NAME" '[.[] | select(.displayName == $name)] | length' <<<"$budgets_json")"
if [[ "$matching_count" -gt 1 ]]; then
  echo "More than one Budget is named ${BUDGET_DISPLAY_NAME}; refusing to guess." >&2
  exit 1
fi
if [[ "$matching_count" -eq 0 ]]; then
  budget_json="$(gcloud billing budgets create \
    --billing-account="$BILLING_ACCOUNT_ID" \
    --billing-project="$FINOPS_PROJECT_ID" \
    --display-name="$BUDGET_DISPLAY_NAME" \
    --budget-amount=1000TWD \
    --calendar-period=month \
    --credit-types-treatment=exclude-all-credits \
    --filter-projects="projects/${TARGET_PROJECT_ID}" \
    --threshold-rule=percent=0.50,basis=current-spend \
    --threshold-rule=percent=0.80,basis=current-spend \
    --threshold-rule=percent=0.90,basis=current-spend \
    --threshold-rule=percent=1.00,basis=current-spend \
    --notifications-rule-pubsub-topic="$topic_name" \
    "${gcloud_args[@]}" --format=json)"
else
  budget_name="$(jq -r --arg name "$BUDGET_DISPLAY_NAME" '.[] | select(.displayName == $name) | .name' <<<"$budgets_json")"
  budget_json="$(gcloud billing budgets describe "$budget_name" --billing-project="$FINOPS_PROJECT_ID" \
    "${gcloud_args[@]}" --format=json)"
fi

jq -e \
  --arg project "projects/${TARGET_PROJECT_NUMBER}" \
  --arg topic "$topic_name" \
  '.amount.specifiedAmount.currencyCode == "TWD"
   and .amount.specifiedAmount.units == "1000"
   and .budgetFilter.calendarPeriod == "MONTH"
   and .budgetFilter.creditTypesTreatment == "EXCLUDE_ALL_CREDITS"
   and .budgetFilter.projects == [$project]
   and ((.allUpdatesRule.pubsubTopic // .notificationsRule.pubsubTopic) == $topic)
   and ((.allUpdatesRule.schemaVersion // .notificationsRule.schemaVersion) == "1.0")
   and ([.thresholdRules[] | select(.spendBasis == "CURRENT_SPEND") | .thresholdPercent] | sort == [0.5, 0.8, 0.9, 1])' \
  <<<"$budget_json" >/dev/null || {
    echo 'Existing/created Budget does not exactly match the approved configuration.' >&2
    jq '{name, displayName, amount, budgetFilter, thresholdRules, allUpdatesRule, notificationsRule}' <<<"$budget_json" >&2
    exit 1
  }

budget_name="$(jq -r '.name' <<<"$budget_json")"
budget_id="${budget_name##*/}"

if gcloud run services describe "$SERVICE" --region="$REGION" --project="$FINOPS_PROJECT_ID" \
  "${gcloud_args[@]}" >/dev/null 2>&1; then
  echo 'Cloud Run service already exists. Refusing an unapproved paid source rebuild.' >&2
  exit 1
fi

echo 'Starting the single approved source build/deploy (CLI deadline: 20 minutes)...'
run_with_timeout 1200 gcloud run deploy "$SERVICE" \
  --source="$SOURCE_DIR" \
  --function=budgetKillSwitch \
  --base-image=nodejs24 \
  --region="$REGION" \
  --project="$FINOPS_PROJECT_ID" \
  --service-account="$runtime_sa" \
  --build-service-account="projects/${FINOPS_PROJECT_ID}/serviceAccounts/${build_sa}" \
  --cpu=1 --memory=512Mi --cpu-throttling \
  --concurrency=1 --min=0 --min-instances=0 --max-instances=1 \
  --timeout=60s --ingress=internal --no-allow-unauthenticated \
  --set-env-vars="TARGET_PROJECT_ID=${TARGET_PROJECT_ID},EXPECTED_BILLING_ACCOUNT_ID=${BILLING_ACCOUNT_ID},EXPECTED_BUDGET_ID=${budget_id},EXPECTED_CURRENCY=TWD,EXPECTED_BUDGET_AMOUNT=1000,DRY_RUN=true" \
  --labels="component=budget-kill-switch,mode=dry-run" \
  "${gcloud_args[@]}"

# gcloud 515 supports revision-level --max-instances but predates the
# service-level --max flag. Cloud Run functions otherwise default the service
# cap to 20, so lower it through the v2 API before event delivery is enabled.
service_json="$(gcloud run services describe "$SERVICE" --region="$REGION" \
  --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}" --format=json)"
service_max="$(jq -r '.metadata.annotations["run.googleapis.com/maxScale"] // ""' <<<"$service_json")"
if [[ "$service_max" != '1' ]]; then
  access_token="$(gcloud auth print-access-token --account="$ACCOUNT")"
  service_endpoint="https://run.googleapis.com/v2/projects/${FINOPS_PROJECT_ID}/locations/${REGION}/services/${SERVICE}?updateMask=scaling.maxInstanceCount"
  service_body="{\"name\":\"projects/${FINOPS_PROJECT_ID}/locations/${REGION}/services/${SERVICE}\",\"scaling\":{\"maxInstanceCount\":1}}"
  curl --silent --show-error --fail-with-body --max-time 60 --retry 0 \
    -X PATCH "${service_endpoint}&validateOnly=true" \
    -H "Authorization: Bearer ${access_token}" -H 'Content-Type: application/json' \
    --data "$service_body" >/dev/null
  curl --silent --show-error --fail-with-body --max-time 60 --retry 0 \
    -X PATCH "$service_endpoint" \
    -H "Authorization: Bearer ${access_token}" -H 'Content-Type: application/json' \
    --data "$service_body" >/dev/null

  service_max=''
  for _ in 1 2 3 4 5 6; do
    service_json="$(gcloud run services describe "$SERVICE" --region="$REGION" \
      --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}" --format=json)"
    service_max="$(jq -r '.metadata.annotations["run.googleapis.com/maxScale"] // ""' <<<"$service_json")"
    [[ "$service_max" == '1' ]] && break
    sleep 5
  done
  [[ "$service_max" == '1' ]] || {
    echo 'Service-level max instances did not reconcile to 1.' >&2
    exit 1
  }
fi

revisions_json="$(gcloud run revisions list --region="$REGION" --project="$FINOPS_PROJECT_ID" \
  "${gcloud_args[@]}" --format=json)"
jq -e --arg service "$SERVICE" '
  [.[] | select(.metadata.labels["serving.knative.dev/service"] == $service)]
  | length > 0 and all(.[];
      (.metadata.annotations["autoscaling.knative.dev/minScale"] // "0") == "0"
      and .metadata.annotations["autoscaling.knative.dev/maxScale"] == "1"
      and .metadata.annotations["run.googleapis.com/cpu-throttling"] == "true"
      and .spec.containers[0].resources.limits.cpu == "1"
      and .spec.containers[0].resources.limits.memory == "512Mi"
      and .spec.containerConcurrency == 1)
' <<<"$revisions_json" >/dev/null || {
  echo 'One or more function revisions violate the approved cost settings.' >&2
  exit 1
}

gcloud run services add-iam-policy-binding "$SERVICE" \
  --region="$REGION" --project="$FINOPS_PROJECT_ID" \
  --member="serviceAccount:${trigger_sa}" --role=roles/run.invoker \
  "${gcloud_args[@]}" >/dev/null

if gcloud eventarc triggers describe "$TRIGGER" --location="$REGION" --project="$FINOPS_PROJECT_ID" \
  "${gcloud_args[@]}" >/dev/null 2>&1; then
  echo 'Eventarc trigger already exists; refusing to reuse an unverified partial trigger.' >&2
  exit 1
fi
run_with_timeout 600 gcloud eventarc triggers create "$TRIGGER" \
  --location="$REGION" --project="$FINOPS_PROJECT_ID" \
  --destination-run-service="$SERVICE" --destination-run-region="$REGION" \
  --event-filters="type=google.cloud.pubsub.topic.v1.messagePublished" \
  --transport-topic="$topic_name" --service-account="$trigger_sa" \
  "${gcloud_args[@]}"

subscription_name="$(gcloud eventarc triggers describe "$TRIGGER" --location="$REGION" \
  --project="$FINOPS_PROJECT_ID" "${gcloud_args[@]}" --format='value(transport.pubsub.subscription)')"
if [[ -z "$subscription_name" ]]; then
  echo 'Could not resolve the Eventarc-managed Pub/Sub subscription.' >&2
  exit 1
fi
subscription_id="${subscription_name##*/}"
pubsub_service_agent="service-$(gcloud projects describe "$FINOPS_PROJECT_ID" "${gcloud_args[@]}" --format='value(projectNumber)')@gcp-sa-pubsub.iam.gserviceaccount.com"
gcloud pubsub topics add-iam-policy-binding "$DLQ_TOPIC" --project="$FINOPS_PROJECT_ID" \
  --member="serviceAccount:${pubsub_service_agent}" --role=roles/pubsub.publisher \
  "${gcloud_args[@]}" >/dev/null
gcloud pubsub subscriptions add-iam-policy-binding "$subscription_id" --project="$FINOPS_PROJECT_ID" \
  --member="serviceAccount:${pubsub_service_agent}" --role=roles/pubsub.subscriber \
  "${gcloud_args[@]}" >/dev/null
gcloud pubsub subscriptions update "$subscription_id" --project="$FINOPS_PROJECT_ID" \
  --message-retention-duration=1h --min-retry-delay=10s --max-retry-delay=60s \
  --dead-letter-topic="$dlq_topic_name" --max-delivery-attempts=5 \
  "${gcloud_args[@]}"

topic_policy="$(gcloud pubsub topics get-iam-policy "$TOPIC" --project="$FINOPS_PROJECT_ID" \
  "${gcloud_args[@]}" --format=json)"
jq -e '[.bindings[]? | select(.role == "roles/pubsub.publisher") | .members[]?]
  | index("serviceAccount:billing-budget-alert@system.gserviceaccount.com") != null' \
  <<<"$topic_policy" >/dev/null || {
    echo 'Budget topic has no publisher binding after Budget creation.' >&2
    exit 1
  }

printf 'Dry-run deployment created. Budget ID: %s\n' "$budget_id"
printf 'Eventarc subscription: %s\n' "$subscription_id"
echo 'No Billing unlink was executed. Run smoke-test.sh, then perform post-deploy inventory before considering production arming.'
