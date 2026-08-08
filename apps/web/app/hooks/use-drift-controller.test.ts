import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  createDriftControllerRequestGuard,
  scheduleDriftPoll,
  scheduleDriftSearchCommit,
} from './use-drift-controller';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
}

afterEach(() => {
  vi.useRealTimers();
});

describe('drift controller timers', () => {
  it('commits only the latest debounced, trimmed query', () => {
    vi.useFakeTimers();
    const commit = vi.fn();

    const cancelFirst = scheduleDriftSearchCommit(' first ', commit, 250);
    vi.advanceTimersByTime(100);
    cancelFirst();

    const cancelLatest = scheduleDriftSearchCommit(' latest ', commit, 250);
    vi.advanceTimersByTime(249);
    expect(commit).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(commit).toHaveBeenCalledOnce();
    expect(commit).toHaveBeenCalledWith('latest');
    cancelLatest();
  });

  it('polls at the requested interval and stops after cleanup', () => {
    vi.useFakeTimers();
    const poll = vi.fn();
    const stopPolling = scheduleDriftPoll(poll, 1_000);

    vi.advanceTimersByTime(3_000);
    expect(poll).toHaveBeenCalledTimes(3);

    stopPolling();
    vi.advanceTimersByTime(5_000);
    expect(poll).toHaveBeenCalledTimes(3);
  });
});

describe('drift controller request guard', () => {
  it('rejects a stale response after a newer request starts', async () => {
    const guard = createDriftControllerRequestGuard();
    const first = deferred<string>();
    const latest = deferred<string>();
    const applied: string[] = [];

    const firstGeneration = guard.beginRequest();
    const applyFirst = first.promise.then((value) => {
      if (guard.isCurrent(firstGeneration)) applied.push(value);
    });

    const latestGeneration = guard.beginRequest();
    const applyLatest = latest.promise.then((value) => {
      if (guard.isCurrent(latestGeneration)) applied.push(value);
    });

    first.resolve('stale');
    await applyFirst;
    expect(applied).toEqual([]);

    latest.resolve('latest');
    await applyLatest;
    expect(applied).toEqual(['latest']);
  });

  it('rejects a response after the controller is disposed', async () => {
    const guard = createDriftControllerRequestGuard();
    const response = deferred<string>();
    const applied: string[] = [];
    const generation = guard.beginRequest();
    const applyResponse = response.promise.then((value) => {
      if (guard.isCurrent(generation)) applied.push(value);
    });

    guard.dispose();
    response.resolve('after-unmount');
    await applyResponse;

    expect(guard.isActive()).toBe(false);
    expect(applied).toEqual([]);
  });
});
