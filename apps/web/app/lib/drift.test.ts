import { describe, expect, it } from 'vitest';

import type { DriftAction } from '../types/drift';
import {
  createDriftActionListResponseGuard,
  driftActionFailedPaths,
  driftActionPaths,
  driftActionPercent,
  driftMethodLabel,
  driftStatusLabel,
  formatUsdRange,
  isDriftActionTerminal,
  markDriftActionRetrying,
  upsertDriftAction,
} from './drift';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

const action: DriftAction = {
  id: 'action-1',
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
};

describe('drift helpers', () => {
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

  it('keeps action order stable while polling and prepends newly started actions', () => {
    const other = { ...action, id: 'action-2' };
    expect(
      upsertDriftAction([action, other], { ...action, progress: 4 })
    ).toEqual([{ ...action, progress: 4 }, other]);
    expect(
      upsertDriftAction([action], other, true).map((item) => item.id)
    ).toEqual(['action-2', 'action-1']);
  });

  it('keeps polling after an explicit retry returns the previous terminal record', () => {
    expect(
      markDriftActionRetrying({ ...action, error: 'precondition' })
    ).toEqual({
      ...action,
      status: 'pending',
      error: undefined,
    });
  });

  it('rejects an older list response after a later action mutation completes', async () => {
    const guard = createDriftActionListResponseGuard();
    const listResponse = deferred<DriftAction[]>();
    const token = guard.beginRequest();
    let current = [action];
    const applyList = listResponse.promise.then((actions) => {
      if (guard.isCurrent(token)) current = actions;
    });

    guard.markMutation();
    current = upsertDriftAction(current, { ...action, progress: 4 });
    listResponse.resolve([{ ...action, progress: 1 }]);
    await applyList;

    expect(current[0].progress).toBe(4);
  });

  it('rejects an older list response after a newer list request starts', async () => {
    const guard = createDriftActionListResponseGuard();
    const listResponse = deferred<DriftAction[]>();
    const token = guard.beginRequest();
    let current = [action];
    const applyList = listResponse.promise.then((actions) => {
      if (guard.isCurrent(token)) current = actions;
    });

    guard.beginRequest();
    listResponse.resolve([{ ...action, progress: 1 }]);
    await applyList;

    expect(current[0].progress).toBe(3);
  });

  it('formats estimated cost as a range', () => {
    expect(formatUsdRange(0.25, 0.5)).toBe('$0.25–$0.50');
    expect(formatUsdRange(0.001, 0.001)).toBe('< US$0.01');
  });

  it('labels diagnostic statuses and plan methods from the API contract', () => {
    expect(driftStatusLabel('target_conflict')).toBe('Target conflict');
    expect(driftStatusLabel('shared_object')).toBe('Shared object');
    expect(driftMethodLabel('copy_verify_delete')).toBe(
      'Copy · verify · delete'
    );
    expect(driftMethodLabel('atomic_move')).toBe('Atomic move');
    expect(driftMethodLabel('copy_on_branch')).toBe('Copy on shared branch');
  });
});
