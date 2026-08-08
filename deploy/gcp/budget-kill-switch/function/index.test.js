'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');

const {createHandler} = require('./index');

const ENV = Object.freeze({
  TARGET_PROJECT_ID: 'storage-403503',
  EXPECTED_BILLING_ACCOUNT_ID: '01420A-B7869F-A617B2',
  EXPECTED_BUDGET_ID: 'budget-123',
  EXPECTED_CURRENCY: 'TWD',
  EXPECTED_BUDGET_AMOUNT: '1000',
  DRY_RUN: 'true',
});

function event(overrides = {}) {
  const payload = {
    budgetDisplayName: 'storage-403503-monthly-kill-switch-twd-1000',
    costAmount: 1000,
    costIntervalStart: '2026-08-01T00:00:00Z',
    budgetAmount: 1000,
    budgetAmountType: 'SPECIFIED_AMOUNT',
    currencyCode: 'TWD',
    ...overrides.payload,
  };
  return {
    id: 'event-1',
    data: {
      message: {
        messageId: 'message-1',
        attributes: {
          billingAccountId: '01420A-B7869F-A617B2',
          budgetId: 'budget-123',
          schemaVersion: '1.0',
          ...overrides.attributes,
        },
        data: overrides.data ?? Buffer.from(JSON.stringify(payload)).toString('base64'),
      },
    },
  };
}

function billingClient({enabled = true, account = 'billingAccounts/01420A-B7869F-A617B2'} = {}) {
  const calls = {get: [], update: []};
  return {
    calls,
    async getProjectBillingInfo(request) {
      calls.get.push(request);
      return [{billingEnabled: enabled, billingAccountName: enabled ? account : ''}];
    },
    async updateProjectBillingInfo(request) {
      calls.update.push(request);
      return [{}];
    },
  };
}

function handler(client, env = ENV) {
  return createHandler({
    billingClient: client,
    env,
    now: () => new Date('2026-08-08T12:00:00Z'),
  });
}

test('低於 TWD 1000 不讀取或修改 Billing', async () => {
  const client = billingClient();
  const result = await handler(client)(event({payload: {costAmount: 999.99}}));
  assert.deepEqual(result, {status: 'below_limit'});
  assert.equal(client.calls.get.length, 0);
  assert.equal(client.calls.update.length, 0);
});

test('剛好 TWD 1000 在 dry-run 模擬熔斷但不修改 Billing', async () => {
  const client = billingClient();
  const result = await handler(client)(event());
  assert.deepEqual(result, {status: 'dry_run'});
  assert.equal(client.calls.get.length, 1);
  assert.equal(client.calls.update.length, 0);
});

test('超過門檻在 production 模式 unlink 指定 project', async () => {
  const client = billingClient();
  const result = await handler(client, {...ENV, DRY_RUN: 'false'})(
    event({payload: {costAmount: 1000.01}}),
  );
  assert.deepEqual(result, {status: 'disabled'});
  assert.deepEqual(client.calls.update, [{
    name: 'projects/storage-403503',
    projectBillingInfo: {billingAccountName: ''},
  }]);
});

test('已停用 Billing 時是冪等 no-op', async () => {
  const client = billingClient({enabled: false});
  const result = await handler(client, {...ENV, DRY_RUN: 'false'})(event());
  assert.deepEqual(result, {status: 'already_disabled'});
  assert.equal(client.calls.update.length, 0);
});

test('project 被連到不同 Billing Account 時 fail closed', async () => {
  const client = billingClient({account: 'billingAccounts/WRONG'});
  const result = await handler(client, {...ENV, DRY_RUN: 'false'})(event());
  assert.deepEqual(result, {status: 'ignored', reason: 'billing_account_mismatch'});
  assert.equal(client.calls.update.length, 0);
});

for (const [name, badEvent, reason] of [
  ['錯誤 Billing Account', event({attributes: {billingAccountId: 'WRONG'}}), 'unexpected_billing_account'],
  ['錯誤 Budget ID', event({attributes: {budgetId: 'WRONG'}}), 'unexpected_budget'],
  ['錯誤 schema', event({attributes: {schemaVersion: '2.0'}}), 'unexpected_schema'],
  ['錯誤幣別', event({payload: {currencyCode: 'USD'}}), 'unexpected_currency'],
  ['錯誤預算金額', event({payload: {budgetAmount: 999}}), 'unexpected_budget_amount'],
  ['過期月份', event({payload: {costIntervalStart: '2026-06-01T00:00:00Z'}}), 'stale_interval'],
  ['malformed base64', event({data: 'not base64!'}), 'invalid_base64'],
  ['malformed JSON', event({data: Buffer.from('{').toString('base64')}), 'invalid_json'],
]) {
  test(`${name} 會 ack 並且零 mutation`, async () => {
    const client = billingClient();
    const result = await handler(client)(badEvent);
    assert.deepEqual(result, {status: 'ignored', reason});
    assert.equal(client.calls.get.length, 0);
    assert.equal(client.calls.update.length, 0);
  });
}

test('forecast threshold 本身不會觸發，仍只看 actual cost', async () => {
  const client = billingClient();
  const result = await handler(client)(event({
    payload: {costAmount: 800, forecastThresholdExceeded: 1.0},
  }));
  assert.deepEqual(result, {status: 'below_limit'});
  assert.equal(client.calls.get.length, 0);
});

test('接受時區偏移後仍屬於當月的 interval start', async () => {
  const client = billingClient();
  const result = await handler(client)(event({
    payload: {costAmount: 999, costIntervalStart: '2026-07-31T16:00:00Z'},
  }));
  assert.deepEqual(result, {status: 'below_limit'});
  assert.equal(client.calls.get.length, 0);
});

test('拒絕未來月份的 out-of-order 通知', async () => {
  const client = billingClient();
  const result = await handler(client)(event({
    payload: {costIntervalStart: '2026-09-01T00:00:00Z'},
  }));
  assert.deepEqual(result, {status: 'ignored', reason: 'stale_interval'});
  assert.equal(client.calls.get.length, 0);
  assert.equal(client.calls.update.length, 0);
});

test('重複 exact-threshold 通知保持安全且不執行 dry-run mutation', async () => {
  const client = billingClient();
  const run = handler(client);
  assert.deepEqual(await run(event()), {status: 'dry_run'});
  assert.deepEqual(await run(event()), {status: 'dry_run'});
  assert.equal(client.calls.get.length, 2);
  assert.equal(client.calls.update.length, 0);
});

test('Billing API 暫時錯誤會向上拋出以交給 Pub/Sub 有限重試', async () => {
  const client = billingClient();
  client.getProjectBillingInfo = async () => {
    throw new Error('temporary API failure');
  };
  await assert.rejects(handler(client)(event()), /temporary API failure/);
  assert.equal(client.calls.update.length, 0);
});

test('DRY_RUN 必須是明確布林字串', async () => {
  const client = billingClient();
  await assert.rejects(handler(client, {...ENV, DRY_RUN: 'yes'})(event()), /DRY_RUN/);
  assert.equal(client.calls.get.length, 0);
  assert.equal(client.calls.update.length, 0);
});
