import { describe, expect, it } from 'vitest';

import type { DriftAction } from '../types/drift';
import {
  driftActionFailedPaths,
  driftActionPercent,
  driftMethodLabel,
  driftStatusLabel,
  formatUsdRange,
  isDriftActionTerminal,
} from './drift';

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
