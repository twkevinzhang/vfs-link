import { afterEach, describe, expect, it, vi } from 'vitest';

import type { ShareRecord, ShareStatus } from '../domain/share';
import {
  ShareRequestTimeoutError,
  createShareRequestCoordinator,
  isTerminalShareStatus,
  settleShareRequest,
} from './share-request-coordinator';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
}

function share(status: ShareStatus): ShareRecord {
  return {
    id: 'share-1',
    logicPath: 'docs/report.pdf',
    fileName: 'report.pdf',
    size: 1,
    destinationObject: 'shares/report.pdf',
    destinationUrl: 'gs://bucket/shares/report.pdf',
    shareUrl: 'https://example.test/report.pdf',
    email: '',
    notificationTarget: '',
    status,
    createdAt: '2026-08-09T00:00:00Z',
    updatedAt: '2026-08-09T00:00:00Z',
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe('share request coordinator', () => {
  it('keeps at most one polling request in flight', async () => {
    const pending = deferred<ShareRecord>();
    const load = vi.fn(() => pending.promise);
    const applied: ShareRecord[] = [];
    const coordinator = createShareRequestCoordinator({
      load,
      onSuccess: (next) => applied.push(next),
    });

    const first = coordinator.poll();
    const duplicate = coordinator.poll();

    expect(load).toHaveBeenCalledOnce();
    pending.resolve(share('uploading'));
    await expect(Promise.all([first, duplicate])).resolves.toEqual([
      share('uploading'),
      share('uploading'),
    ]);
    expect(applied).toEqual([share('uploading')]);
  });

  it('ignores a stale response when refresh supersedes polling', async () => {
    const stale = deferred<ShareRecord>();
    const latest = deferred<ShareRecord>();
    const load = vi
      .fn<(_signal: AbortSignal) => Promise<ShareRecord>>()
      .mockImplementationOnce(() => stale.promise)
      .mockImplementationOnce(() => latest.promise);
    const applied: ShareStatus[] = [];
    const coordinator = createShareRequestCoordinator({
      load,
      onSuccess: (next) => applied.push(next.status),
    });

    const staleRequest = coordinator.poll();
    const latestRequest = coordinator.refresh();
    latest.resolve(share('completed'));
    await latestRequest;
    stale.resolve(share('uploading'));
    await staleRequest;

    expect(applied).toEqual(['completed']);
  });

  it('stops automatic polling after a terminal response', async () => {
    const load = vi.fn().mockResolvedValue(share('completed'));
    const coordinator = createShareRequestCoordinator({
      load,
      onSuccess: vi.fn(),
    });

    await coordinator.poll();
    await expect(coordinator.poll()).resolves.toBeUndefined();

    expect(load).toHaveBeenCalledOnce();
  });

  it('can resume polling after a user action invalidates terminal state', async () => {
    const load = vi
      .fn()
      .mockResolvedValueOnce(share('failed'))
      .mockResolvedValueOnce(share('uploading'));
    const coordinator = createShareRequestCoordinator({
      load,
      onSuccess: vi.fn(),
    });

    await coordinator.poll();
    coordinator.cancel();
    await coordinator.poll();

    expect(load).toHaveBeenCalledTimes(2);
  });

  it('aborts active work and rejects no state after disposal', async () => {
    let receivedSignal: AbortSignal | undefined;
    const onSuccess = vi.fn();
    const load = vi.fn(
      (signal: AbortSignal) =>
        new Promise<ShareRecord>((_resolve, reject) => {
          receivedSignal = signal;
          signal.addEventListener(
            'abort',
            () => reject(new DOMException('Aborted', 'AbortError')),
            { once: true }
          );
        })
    );
    const coordinator = createShareRequestCoordinator({ load, onSuccess });

    const request = coordinator.poll();
    coordinator.dispose();

    await expect(request).resolves.toBeUndefined();
    expect(receivedSignal?.aborted).toBe(true);
    expect(onSuccess).not.toHaveBeenCalled();
  });

  it('enforces a hard request deadline', async () => {
    vi.useFakeTimers();
    const errors: unknown[] = [];
    const load = vi.fn(
      (signal: AbortSignal) =>
        new Promise<ShareRecord>((_resolve, reject) => {
          signal.addEventListener(
            'abort',
            () => reject(new DOMException('Aborted', 'AbortError')),
            { once: true }
          );
        })
    );
    const coordinator = createShareRequestCoordinator({
      load,
      onSuccess: vi.fn(),
      onError: (error) => errors.push(error),
      deadlineMs: 5_000,
    });

    const request = coordinator.poll();
    const assertion = expect(request).rejects.toBeInstanceOf(
      ShareRequestTimeoutError
    );
    await vi.advanceTimersByTimeAsync(5_000);
    await assertion;
    expect(errors[0]).toBeInstanceOf(ShareRequestTimeoutError);
  });

  it('ignores and aborts a stale start response superseded by refresh', async () => {
    const staleStart = deferred<ShareRecord>();
    const latestLoad = deferred<ShareRecord>();
    let startSignal: AbortSignal | undefined;
    const applied: ShareStatus[] = [];
    const coordinator = createShareRequestCoordinator({
      load: () => latestLoad.promise,
      start: (signal) => {
        startSignal = signal;
        return staleStart.promise;
      },
      onSuccess: (next) => applied.push(next.status),
    });

    const startRequest = coordinator.start();
    const refreshRequest = coordinator.refresh();
    expect(startSignal?.aborted).toBe(true);

    latestLoad.resolve(share('uploading'));
    await refreshRequest;
    staleStart.resolve(share('completed'));
    await startRequest;

    expect(applied).toEqual(['uploading']);
  });

  it('aborts start and applies no state after disposal', async () => {
    let startSignal: AbortSignal | undefined;
    const onSuccess = vi.fn();
    const coordinator = createShareRequestCoordinator({
      load: vi.fn(),
      start: (signal) =>
        new Promise<ShareRecord>((_resolve, reject) => {
          startSignal = signal;
          signal.addEventListener(
            'abort',
            () => reject(new DOMException('Aborted', 'AbortError')),
            { once: true }
          );
        }),
      onSuccess,
    });

    const request = coordinator.start();
    coordinator.dispose();

    await expect(request).resolves.toBeUndefined();
    expect(startSignal?.aborted).toBe(true);
    expect(onSuccess).not.toHaveBeenCalled();
  });

  it('settles a reported manual refresh failure without leaking a rejection', async () => {
    await expect(
      settleShareRequest(Promise.reject(new Error('refresh failed')))
    ).resolves.toBeUndefined();
  });
});

describe('isTerminalShareStatus', () => {
  it.each<ShareStatus>([
    'completed',
    'notified',
    'notification_failed',
    'email_sent',
    'failed',
    'email_failed',
  ])('recognizes %s as terminal', (status) => {
    expect(isTerminalShareStatus(status)).toBe(true);
  });

  it.each<ShareStatus>(['draft', 'uploading'])(
    'keeps polling for %s',
    (status) => {
      expect(isTerminalShareStatus(status)).toBe(false);
    }
  );
});
