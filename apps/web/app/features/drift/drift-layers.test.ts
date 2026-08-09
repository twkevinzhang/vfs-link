import { describe, expect, it } from 'vitest';

import type { DriftAction, DriftItem } from './domain/drift';
import { createDriftActionListResponseGuard } from './application/drift-action-list-guard';
import {
  driftActionFailedPaths,
  driftActionPaths,
  isActionableDriftItem,
  isDriftActionTerminal,
  markDriftActionRetrying,
  upsertDriftAction,
} from './domain/drift-policy';
import {
  driftActionPercent,
  driftMethodLabel,
  driftStatusLabel,
  formatUsdRange,
} from './presentation/drift-formatters';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

const action: DriftAction = {
  id: 'action-1',
  idempotencyKey: 'key-1',
  planId: 'plan-1',
  status: 'partial',
  progress: 3,
  total: 4,
  succeeded: 2,
  failed: 1,
  results: [
    { logicPath: 'ok.txt', status: 'completed' },
    { logicPath: 'retry.txt', status: 'failed', error: 'precondition' },
  ],
  createdAt: '2026-08-09T00:00:00Z',
  updatedAt: '2026-08-09T00:00:00Z',
};

describe('drift layers', () => {
  it('uses the server-authoritative actionable flag without status inference', () => {
    const item = {
      logicPath: 'archive/report.pdf',
      currentKey: 'object-key',
      targetKey: 'archive/report.pdf',
      status: 'shared_object',
      size: 10,
      storageClass: 'ARCHIVE',
      generation: 42,
      estimatedCostUsdMin: 0,
      estimatedCostUsdMax: 0,
      actionable: false,
    } satisfies DriftItem;

    expect(isActionableDriftItem(item)).toBe(false);
    expect(
      isActionableDriftItem({ ...item, status: 'unknown', actionable: true })
    ).toBe(true);
  });

  it('identifies terminal action states and computes bounded progress', () => {
    expect(isDriftActionTerminal('partial')).toBe(true);
    expect(isDriftActionTerminal('running')).toBe(false);
    expect(driftActionPercent(action)).toBe(75);
    expect(driftActionPercent({ ...action, progress: 8 })).toBe(100);
  });

  it('returns only failed paths for retry', () => {
    expect(driftActionFailedPaths(action)).toEqual(['retry.txt']);
    expect(driftActionPaths(action)).toEqual(['ok.txt', 'retry.txt']);
  });

  it('keeps action order stable while polling and prepends new actions', () => {
    const other = { ...action, id: 'action-2' };
    expect(
      upsertDriftAction([action, other], { ...action, progress: 4 })
    ).toEqual([{ ...action, progress: 4 }, other]);
    expect(
      upsertDriftAction([action], other, true).map((item) => item.id)
    ).toEqual(['action-2', 'action-1']);
  });

  it('keeps polling after retry returns the previous terminal record', () => {
    expect(
      markDriftActionRetrying({ ...action, error: 'precondition' })
    ).toEqual({ ...action, status: 'pending', error: undefined });
  });

  it('rejects an older list response after a mutation', async () => {
    const guard = createDriftActionListResponseGuard();
    const response = deferred<DriftAction[]>();
    const token = guard.beginRequest();
    let current = [action];
    const apply = response.promise.then((actions) => {
      if (guard.isCurrent(token)) current = actions;
    });
    guard.markMutation();
    current = upsertDriftAction(current, { ...action, progress: 4 });
    response.resolve([{ ...action, progress: 1 }]);
    await apply;
    expect(current[0].progress).toBe(4);
  });

  it('rejects an older list response after a newer request', async () => {
    const guard = createDriftActionListResponseGuard();
    const response = deferred<DriftAction[]>();
    const token = guard.beginRequest();
    let current = [action];
    const apply = response.promise.then((actions) => {
      if (guard.isCurrent(token)) current = actions;
    });
    guard.beginRequest();
    response.resolve([{ ...action, progress: 1 }]);
    await apply;
    expect(current[0].progress).toBe(3);
  });

  it('formats estimated cost as a range', () => {
    expect(formatUsdRange(0.25, 0.5)).toBe('$0.25–$0.50');
    expect(formatUsdRange(0.001, 0.001)).toBe('< US$0.01');
  });

  it('labels statuses and plan methods from the API contract', () => {
    expect(driftStatusLabel('target_conflict')).toBe('Target conflict');
    expect(driftStatusLabel('shared_object')).toBe('Shared object');
    expect(driftMethodLabel('copy_verify_delete')).toBe(
      'Copy · verify · delete'
    );
    expect(driftMethodLabel('atomic_move')).toBe('Atomic move');
    expect(driftMethodLabel('copy_on_branch')).toBe('Copy on shared branch');
  });
});
