'use strict';

const functions = require('@google-cloud/functions-framework');
const {CloudBillingClient} = require('@google-cloud/billing');

const HOUR_MS = 60 * 60 * 1000;

function log(severity, event, fields = {}) {
  console.log(JSON.stringify({severity, event, ...fields}));
}

function parseBoolean(value, name) {
  if (value === 'true') return true;
  if (value === 'false') return false;
  throw new Error(`${name} must be exactly "true" or "false"`);
}

function loadConfig(env) {
  const required = [
    'TARGET_PROJECT_ID',
    'EXPECTED_BILLING_ACCOUNT_ID',
    'EXPECTED_BUDGET_ID',
    'EXPECTED_CURRENCY',
    'EXPECTED_BUDGET_AMOUNT',
    'DRY_RUN',
  ];
  for (const name of required) {
    if (!env[name]) throw new Error(`Missing required environment variable ${name}`);
  }

  const budgetAmount = Number(env.EXPECTED_BUDGET_AMOUNT);
  if (!Number.isFinite(budgetAmount) || budgetAmount <= 0) {
    throw new Error('EXPECTED_BUDGET_AMOUNT must be a positive number');
  }

  return {
    targetProjectId: env.TARGET_PROJECT_ID,
    billingAccountId: env.EXPECTED_BILLING_ACCOUNT_ID,
    budgetId: env.EXPECTED_BUDGET_ID,
    currency: env.EXPECTED_CURRENCY,
    budgetAmount,
    dryRun: parseBoolean(env.DRY_RUN, 'DRY_RUN'),
  };
}

function decodeBudgetMessage(cloudEvent) {
  const message = cloudEvent?.data?.message;
  if (!message || typeof message !== 'object') {
    return {ignored: 'missing_pubsub_message'};
  }

  const attributes = message.attributes;
  if (!attributes || typeof attributes !== 'object') {
    return {ignored: 'missing_attributes'};
  }

  const encoded = message.data;
  if (
    typeof encoded !== 'string' ||
    encoded.length === 0 ||
    encoded.length % 4 !== 0 ||
    !/^[A-Za-z0-9+/]*={0,2}$/.test(encoded)
  ) {
    return {ignored: 'invalid_base64'};
  }

  let payload;
  try {
    payload = JSON.parse(Buffer.from(encoded, 'base64').toString('utf8'));
  } catch {
    return {ignored: 'invalid_json'};
  }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    return {ignored: 'invalid_payload'};
  }

  return {attributes, payload, messageId: message.messageId || null};
}

function validateNotification(decoded, config, now) {
  const {attributes, payload} = decoded;
  if (attributes.schemaVersion !== '1.0') return 'unexpected_schema';
  if (attributes.billingAccountId !== config.billingAccountId) return 'unexpected_billing_account';
  if (attributes.budgetId !== config.budgetId) return 'unexpected_budget';
  if (payload.currencyCode !== config.currency) return 'unexpected_currency';
  if (payload.budgetAmountType !== 'SPECIFIED_AMOUNT') return 'unexpected_budget_type';

  const budgetAmount = Number(payload.budgetAmount);
  const costAmount = Number(payload.costAmount);
  if (!Number.isFinite(budgetAmount) || budgetAmount !== config.budgetAmount) {
    return 'unexpected_budget_amount';
  }
  if (!Number.isFinite(costAmount) || costAmount < 0) return 'invalid_cost_amount';

  const intervalStart = new Date(payload.costIntervalStart);
  if (!Number.isFinite(intervalStart.getTime())) return 'invalid_interval_start';
  const currentMonthStartUtc = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1);
  // A billing calendar month starts at local midnight. Every IANA timezone's
  // UTC offset is within -12h..+14h, so this accepts the current month in any
  // billing timezone without accepting a delayed notification for last month.
  if (Math.abs(intervalStart.getTime() - currentMonthStartUtc) > 14 * HOUR_MS) {
    return 'stale_interval';
  }

  return null;
}

function projectBillingAccountName(config) {
  return `billingAccounts/${config.billingAccountId}`;
}

function createHandler({
  billingClient = new CloudBillingClient(),
  env = process.env,
  now = () => new Date(),
} = {}) {
  return async function budgetKillSwitch(cloudEvent) {
    const config = loadConfig(env);
    const decoded = decodeBudgetMessage(cloudEvent);
    if (decoded.ignored) {
      log('WARNING', 'budget_notification_ignored', {reason: decoded.ignored});
      return {status: 'ignored', reason: decoded.ignored};
    }

    const invalidReason = validateNotification(decoded, config, now());
    if (invalidReason) {
      log('WARNING', 'budget_notification_ignored', {
        reason: invalidReason,
        messageId: decoded.messageId,
      });
      return {status: 'ignored', reason: invalidReason};
    }

    const costAmount = Number(decoded.payload.costAmount);
    if (costAmount < config.budgetAmount) {
      log('INFO', 'budget_below_limit', {
        messageId: decoded.messageId,
        costAmount,
        budgetAmount: config.budgetAmount,
        currency: config.currency,
      });
      return {status: 'below_limit'};
    }

    const projectName = `projects/${config.targetProjectId}`;
    const [billingInfo] = await billingClient.getProjectBillingInfo({name: projectName});
    if (!billingInfo?.billingEnabled || !billingInfo.billingAccountName) {
      log('INFO', 'billing_already_disabled', {
        messageId: decoded.messageId,
        projectId: config.targetProjectId,
      });
      return {status: 'already_disabled'};
    }

    if (billingInfo.billingAccountName !== projectBillingAccountName(config)) {
      log('ERROR', 'billing_account_mismatch', {
        messageId: decoded.messageId,
        projectId: config.targetProjectId,
      });
      return {status: 'ignored', reason: 'billing_account_mismatch'};
    }

    if (config.dryRun) {
      log('CRITICAL', 'billing_disable_simulated', {
        messageId: decoded.messageId,
        projectId: config.targetProjectId,
        costAmount,
        budgetAmount: config.budgetAmount,
        currency: config.currency,
      });
      return {status: 'dry_run'};
    }

    await billingClient.updateProjectBillingInfo({
      name: projectName,
      projectBillingInfo: {billingAccountName: ''},
    });
    log('CRITICAL', 'billing_disabled', {
      messageId: decoded.messageId,
      projectId: config.targetProjectId,
      costAmount,
      budgetAmount: config.budgetAmount,
      currency: config.currency,
    });
    return {status: 'disabled'};
  };
}

const budgetKillSwitch = createHandler();
functions.cloudEvent('budgetKillSwitch', budgetKillSwitch);

module.exports = {
  budgetKillSwitch,
  createHandler,
  decodeBudgetMessage,
  loadConfig,
  validateNotification,
};
