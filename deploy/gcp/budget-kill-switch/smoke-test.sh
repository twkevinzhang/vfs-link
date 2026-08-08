#!/usr/bin/env bash
set -euo pipefail

ACCOUNT="${ACCOUNT:-twkevinzhang@gmail.com}"
FINOPS_PROJECT_ID="${FINOPS_PROJECT_ID:-chromatic-idea-405303}"
BILLING_ACCOUNT_ID="${BILLING_ACCOUNT_ID:-01420A-B7869F-A617B2}"
BUDGET_ID="${BUDGET_ID:-}"
TOPIC="${TOPIC:-budget-kill-events}"
CONFIRM_SMOKE="${CONFIRM_SMOKE:-}"

if [[ -z "$BUDGET_ID" ]]; then
  echo 'BUDGET_ID is required.' >&2
  exit 1
fi
if [[ "$CONFIRM_SMOKE" != "${FINOPS_PROJECT_ID}:DRY_RUN" ]]; then
  echo "Set CONFIRM_SMOKE=${FINOPS_PROJECT_ID}:DRY_RUN to publish two bounded synthetic messages." >&2
  exit 1
fi

interval_start="$(date -u +%Y-%m-01T00:00:00Z)"
attributes="billingAccountId=${BILLING_ACCOUNT_ID},budgetId=${BUDGET_ID},schemaVersion=1.0"

payload_below="{\"budgetDisplayName\":\"storage-403503-monthly-kill-switch-twd-1000\",\"costAmount\":999.99,\"costIntervalStart\":\"${interval_start}\",\"budgetAmount\":1000,\"budgetAmountType\":\"SPECIFIED_AMOUNT\",\"currencyCode\":\"TWD\"}"
payload_exact="{\"budgetDisplayName\":\"storage-403503-monthly-kill-switch-twd-1000\",\"costAmount\":1000,\"costIntervalStart\":\"${interval_start}\",\"budgetAmount\":1000,\"budgetAmountType\":\"SPECIFIED_AMOUNT\",\"currencyCode\":\"TWD\"}"

gcloud pubsub topics publish "$TOPIC" --project="$FINOPS_PROJECT_ID" --account="$ACCOUNT" \
  --attribute="$attributes" --message="$payload_below" --quiet
gcloud pubsub topics publish "$TOPIC" --project="$FINOPS_PROJECT_ID" --account="$ACCOUNT" \
  --attribute="$attributes" --message="$payload_exact" --quiet

echo 'Published exactly two synthetic messages: TWD 999.99 and TWD 1000.00.'
echo 'Expected structured events: budget_below_limit and billing_disable_simulated. No unlink is permitted while DRY_RUN=true.'
