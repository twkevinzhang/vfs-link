#!/usr/bin/env bash
set -euo pipefail

ACCOUNT="${ACCOUNT:-twkevinzhang@gmail.com}"
TARGET_PROJECT_ID="${TARGET_PROJECT_ID:-storage-403503}"
FINOPS_PROJECT_ID="${FINOPS_PROJECT_ID:-chromatic-idea-405303}"
BILLING_ACCOUNT_ID="${BILLING_ACCOUNT_ID:-01420A-B7869F-A617B2}"
REGION="${REGION:-asia-east1}"
EXPECTED_BUDGET_AMOUNT="${EXPECTED_BUDGET_AMOUNT:-1000}"
FX_TWD_PER_USD="${FX_TWD_PER_USD:-32.56}"
OUTPUT="${OUTPUT:-tmp/gcp-budget-kill-switch-preflight-$(date +%Y%m%d-%H%M%S).txt}"

for command_name in gcloud jq; do
  command -v "$command_name" >/dev/null || {
    echo "Required command not found: $command_name" >&2
    exit 1
  }
done

mkdir -p "$(dirname "$OUTPUT")"

gcloud_args=(--account="$ACCOUNT" --quiet)

sanitize_services() {
  local project_id="$1"
  local raw
  if ! raw="$(gcloud run services list --platform=managed --project="$project_id" "${gcloud_args[@]}" --format=json 2>&1)"; then
    printf 'Cloud Run services unavailable: %s\n' "$raw"
    return
  fi
  jq '[.[] | {
    name: .metadata.name,
    region: .metadata.labels["cloud.googleapis.com/location"],
    serviceAnnotations: ((.metadata.annotations // {}) | with_entries(select(.key | test("(scalingMode|maxScale|ingress)$")))),
    templateAnnotations: ((.spec.template.metadata.annotations // {}) | with_entries(select(.key | test("(minScale|maxScale|cpu-throttling|startup-cpu-boost)$")))),
    cpu: .spec.template.spec.containers[0].resources.limits.cpu,
    memory: .spec.template.spec.containers[0].resources.limits.memory,
    gpu: (.spec.template.spec.containers[0].resources.limits["nvidia.com/gpu"] // "0"),
    concurrency: .spec.template.spec.containerConcurrency,
    timeoutSeconds: .spec.template.spec.timeoutSeconds,
    ready: ([.status.conditions[]? | select(.type == "Ready") | .status] | first),
    traffic: .status.traffic
  }]' <<<"$raw"
}

sanitize_revisions() {
  local project_id="$1"
  local services_json regions region raw
  if ! services_json="$(gcloud run services list --platform=managed --project="$project_id" "${gcloud_args[@]}" --format=json 2>/dev/null)"; then
    printf 'Cloud Run revisions unavailable because the service list is unavailable.\n'
    return
  fi
  regions="$(jq -r '.[].metadata.labels["cloud.googleapis.com/location"]' <<<"$services_json" | sort -u)"
  if [[ -z "$regions" ]]; then
    printf '[]\n'
    return
  fi
  while IFS= read -r region; do
    raw="$(gcloud run revisions list --platform=managed --region="$region" --project="$project_id" "${gcloud_args[@]}" --format=json)"
    jq --arg region "$region" '[.[] | {
      name: .metadata.name,
      service: .metadata.labels["serving.knative.dev/service"],
      region: $region,
      active: ([.status.conditions[]? | select(.type == "Active") | .status] | first),
      minScale: (.metadata.annotations["autoscaling.knative.dev/minScale"] // "0"),
      maxScale: (.metadata.annotations["autoscaling.knative.dev/maxScale"] // null),
      cpuThrottling: (.metadata.annotations["run.googleapis.com/cpu-throttling"] // "true"),
      cpu: .spec.containers[0].resources.limits.cpu,
      memory: .spec.containers[0].resources.limits.memory,
      gpu: (.spec.containers[0].resources.limits["nvidia.com/gpu"] // "0"),
      concurrency: .spec.containerConcurrency,
      timeoutSeconds: .spec.timeoutSeconds,
      ready: ([.status.conditions[]? | select(.type == "Ready") | .status] | first)
    }]' <<<"$raw"
  done <<<"$regions"
}

accessible_capacity() {
  local project_id="$1"
  local services_json regions region revisions_json all_revisions='[]'
  if ! services_json="$(gcloud run services list --platform=managed --project="$project_id" "${gcloud_args[@]}" --format=json 2>/dev/null)"; then
    printf 'Cloud Run accessible capacity unavailable because the service list is unavailable.\n'
    return
  fi
  regions="$(jq -r '.[].metadata.labels["cloud.googleapis.com/location"]' <<<"$services_json" | sort -u)"
  while IFS= read -r region; do
    [[ -n "$region" ]] || continue
    revisions_json="$(gcloud run revisions list --platform=managed --region="$region" --project="$project_id" "${gcloud_args[@]}" --format=json)"
    all_revisions="$(jq -n --argjson current "$all_revisions" --argjson additions "$revisions_json" '$current + $additions')"
  done <<<"$regions"

  jq -n --argjson services "$services_json" --argjson revisions "$all_revisions" '
    def mem_gib:
      if . == null then 0
      elif endswith("Gi") then rtrimstr("Gi") | tonumber
      elif endswith("Mi") then (rtrimstr("Mi") | tonumber) / 1024
      else 0 end;
    [
      $services[] as $service
      | $service.status.traffic[]?
      | select(((.percent // 0) > 0) or (.tag != null))
      | . as $traffic
      | $revisions[]
      | select(.metadata.name == $traffic.revisionName)
      | {
          service: $service.metadata.name,
          revision: .metadata.name,
          trafficPercent: ($traffic.percent // 0),
          tag: ($traffic.tag // null),
          serviceMin: (($service.metadata.annotations["run.googleapis.com/minScale"] // "0") | tonumber),
          revisionMin: ((.metadata.annotations["autoscaling.knative.dev/minScale"] // "0") | tonumber),
          cpu: ((.spec.containers[0].resources.limits.cpu // "0") | tonumber),
          memoryGiB: ((.spec.containers[0].resources.limits.memory // "0Gi") | mem_gib)
        }
    ] as $accessible
    | {
        accessibleRevisions: $accessible,
        conservativeStandingCapacity: {
          instances: ([$accessible[] | .serviceMin + .revisionMin] | add // 0),
          vCPU: ([$accessible[] | (.serviceMin + .revisionMin) * .cpu] | add // 0),
          memoryGiB: ([$accessible[] | (.serviceMin + .revisionMin) * .memoryGiB] | add // 0)
        }
      }
  '
}

target_billing="$(gcloud billing projects describe "$TARGET_PROJECT_ID" "${gcloud_args[@]}" --format=json)"
finops_billing="$(gcloud billing projects describe "$FINOPS_PROJECT_ID" "${gcloud_args[@]}" --format=json)"
account_info="$(gcloud billing accounts describe "$BILLING_ACCOUNT_ID" "${gcloud_args[@]}" --format=json)"

expected_account="billingAccounts/${BILLING_ACCOUNT_ID}"
jq -e --arg account "$expected_account" '.billingEnabled == true and .billingAccountName == $account' <<<"$target_billing" >/dev/null
jq -e --arg account "$expected_account" '.billingEnabled == true and .billingAccountName == $account' <<<"$finops_billing" >/dev/null
jq -e '.open == true and .currencyCode == "TWD"' <<<"$account_info" >/dev/null

{
  echo 'GCP Budget Kill Switch — deployment preflight record'
  printf 'Captured: %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')"
  printf 'Operator: %s\n' "$ACCOUNT"
  echo 'External write owner: root/main agent only'
  echo 'Deployment mode approved: DRY_RUN=true'
  echo 'Temporary incremental budget: <= NT$100, <= 60 minutes'
  echo 'Persistent configuration approved: request-based, CPU throttled, 1 vCPU, 512MiB, min=0, max=1, concurrency=1, timeout=60s, no tag'
  echo
  echo '=== Billing account ==='
  jq '{name, displayName, currencyCode, open}' <<<"$account_info"
  echo '=== Target billing link ==='
  jq '{name, projectId, billingAccountName, billingEnabled}' <<<"$target_billing"
  echo '=== FinOps billing link ==='
  jq '{name, projectId, billingAccountName, billingEnabled}' <<<"$finops_billing"
  echo
  echo '=== Target Cloud Run services (sanitized, all regions) ==='
  sanitize_services "$TARGET_PROJECT_ID"
  echo '=== Target Cloud Run revisions (sanitized, every service region) ==='
  sanitize_revisions "$TARGET_PROJECT_ID"
  echo '=== Target traffic/tag-accessible standing capacity ==='
  accessible_capacity "$TARGET_PROJECT_ID"
  echo '=== FinOps Cloud Run services (sanitized, all regions) ==='
  sanitize_services "$FINOPS_PROJECT_ID"
  echo '=== FinOps Cloud Run revisions (sanitized, every service region) ==='
  sanitize_revisions "$FINOPS_PROJECT_ID"
  echo '=== FinOps traffic/tag-accessible standing capacity ==='
  accessible_capacity "$FINOPS_PROJECT_ID"
  echo
  echo '=== Approved new function cost ceiling ==='
  echo 'Official price: Cloud Run Tier 1 active CPU USD 0.000024/vCPU-s; memory USD 0.0000025/GiB-s; requests USD 0.40/million.'
  printf 'FX assumption: 1 USD = NT$%s (conservative preflight value; recheck after deployment).\n' "$FX_TWD_PER_USD"
  echo 'Formula: 3600 × (1 × 0.000024 + 0.5 × 0.0000025) + 60 × 0.40/1000000 = USD 0.090924/hour.'
  echo 'Worst case: NT$2.96/hour; NT$71.05/24h; NT$2,131.55/30d if max=1 stays request-active continuously.'
  echo 'Normal budget notifications are only several events/day and min=0, so expected compute cost is near zero.'
  echo 'Sources: https://cloud.google.com/run/pricing https://cloud.google.com/pubsub/pricing https://cloud.google.com/artifact-registry/pricing https://cloud.google.com/build/pricing'
  echo
  echo '=== Hard boundaries and cleanup ==='
  printf 'Budget: TWD %s, MONTH, EXCLUDE_ALL_CREDITS, target only %s; actual thresholds 50/80/90/100%%.\n' "$EXPECTED_BUDGET_AMOUNT" "$TARGET_PROJECT_ID"
  echo 'Retry: Pub/Sub retention 1h, min backoff 10s, max backoff 60s, max delivery attempts 5, then 7-day DLQ retention.'
  echo 'Build/deploy deadline: one source build, CLI deadline 20m; no automatic paid rebuild.'
  echo 'Stop condition: any unexpected min>0, no CPU throttling, max>1, URL tag, mismatched IAM/budget/link, or cost over approval stops deployment.'
  printf 'Disarm: CONFIRM_DISARM=%s deploy/gcp/budget-kill-switch/disarm.sh\n' "$FINOPS_PROJECT_ID"
  echo 'Rollback/cleanup details: docs/budget-kill-switch.md'
  echo
  echo '=== Production-arm blockers (do not affect DRY_RUN deployment) ==='
  echo 'Billing link lock state and non-Compute active/pending CUD cannot be proven by this CLI preflight.'
  echo 'DRY_RUN=false requires a separate destructive approval after those blockers are checked.'
} >"$OUTPUT"

printf 'Saved sanitized preflight record: %s\n' "$OUTPUT"
