import 'fake-indexeddb/auto';

import { beforeEach, describe, expect, it, vi } from 'vitest';

import { holdUploadQueueLeadership } from '../lib/upload-queue-coordinator';
import type { UploadQueueItem } from '../lib/upload-queue-model';
import type { PersistedUploadItem } from '../lib/upload-queue-storage';
import { saveUploadQueue } from '../lib/upload-queue-storage';
import {
  createUploadQueueLeadershipHandler,
  hydrateUploadQueueLifecycle,
} from './upload-queue-lifecycle';

const DATABASE_NAME = 'vfs-link-upload-queue';

function deleteDatabase() {
  return new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(DATABASE_NAME);
    request.onsuccess = () => resolve();
    request.onerror = () => reject(request.error);
    request.onblocked = () =>
      reject(new Error('Upload queue database blocked'));
  });
}

function persistedItem(
  overrides: Partial<PersistedUploadItem> = {}
): PersistedUploadItem {
  return {
    key: 'upload-1',
    batchId: 'batch-1',
    relativePath: 'source.txt',
    destinationPath: '',
    logicPath: 'source.txt',
    fingerprint: { name: 'source.txt', size: 20, lastModified: 123 },
    contentType: 'text/plain',
    uploadedBytes: 5,
    state: 'uploading',
    overwrite: false,
    localDuplicate: false,
    retryCount: 1,
    retryEligible: true,
    ...overrides,
  };
}

function queueItem(
  key: string,
  state: UploadQueueItem['state']
): UploadQueueItem {
  return {
    ...persistedItem({ key, state }),
    progress: 25,
  };
}

describe('upload queue hook lifecycle', () => {
  beforeEach(async () => {
    await deleteDatabase();
  });

  it('hydrates persisted work after reload and applies restored scheduler state', async () => {
    await saveUploadQueue([persistedItem()], false);
    const applied = vi.fn();
    const markHydrated = vi.fn();
    const persist = vi.fn();
    const preflight = vi.fn();

    await hydrateUploadQueueLifecycle({
      isMounted: () => true,
      apply: applied,
      markHydrated,
      persist,
      preflight,
    });

    expect(applied).toHaveBeenCalledOnce();
    const [items, globallyPaused] = applied.mock.calls[0] as [
      UploadQueueItem[],
      boolean
    ];
    expect(globallyPaused).toBe(false);
    expect(items).toEqual([
      expect.objectContaining({
        key: 'upload-1',
        state: 'paused',
        progress: 25,
        error: '請重新選擇原始檔案以繼續上傳。',
      }),
    ]);
    expect(markHydrated).toHaveBeenCalledOnce();
    expect(persist).toHaveBeenCalledWith(items, false);
    expect(preflight).not.toHaveBeenCalled();
  });

  it('schedules checking work when this tab owns leadership', async () => {
    const controller = new AbortController();
    const states: string[] = [];
    const leaders: boolean[] = [];
    const schedule = vi.fn();
    const preflight = vi.fn();
    const reload = vi.fn();
    const onState = createUploadQueueLeadershipHandler({
      isMounted: () => true,
      isHydrated: () => true,
      items: () => [
        queueItem('checking', 'checking'),
        queueItem('queued', 'queued'),
      ],
      setLeader: (leader) => leaders.push(leader),
      setState: (state) => states.push(state),
      reload,
      schedule,
      preflight,
    });

    const leadership = holdUploadQueueLeadership({
      signal: controller.signal,
      onState,
    });
    await vi.waitFor(() => expect(schedule).toHaveBeenCalledOnce());

    expect(states).toEqual(['leader']);
    expect(leaders).toEqual([true]);
    expect(preflight).toHaveBeenCalledWith(['checking']);
    expect(reload).not.toHaveBeenCalled();

    controller.abort();
    await leadership;
    expect(states).toEqual(['leader', 'stopped']);
    expect(leaders).toEqual([true, false]);
  });

  it('reloads instead of scheduling after a delayed leadership hand-off', async () => {
    const controller = new AbortController();
    let grantLock: () => void = () => undefined;
    const lockGate = new Promise<void>((resolve) => {
      grantLock = resolve;
    });
    const states: string[] = [];
    const leaders: boolean[] = [];
    const schedule = vi.fn();
    const preflight = vi.fn();
    const reload = vi.fn();
    let now = 1_000;
    const onState = createUploadQueueLeadershipHandler({
      now: () => now,
      isMounted: () => true,
      isHydrated: () => true,
      items: () => [queueItem('checking', 'checking')],
      setLeader: (leader) => leaders.push(leader),
      setState: (state) => states.push(state),
      reload,
      schedule,
      preflight,
    });
    const request = vi.fn(
      async (
        _name: string,
        _options: { mode: 'exclusive'; signal: AbortSignal },
        callback: (lock: Lock | null) => Promise<void>
      ) => {
        await lockGate;
        await callback({ name: 'upload', mode: 'exclusive' } as Lock);
      }
    );

    const leadership = holdUploadQueueLeadership({
      locks: { request },
      signal: controller.signal,
      onState,
    });
    expect(states).toEqual(['waiting']);
    now = 2_001;
    grantLock();
    await vi.waitFor(() => expect(reload).toHaveBeenCalledOnce());

    expect(states).toEqual(['waiting', 'leader']);
    expect(leaders).toEqual([false, true]);
    expect(schedule).not.toHaveBeenCalled();
    expect(preflight).not.toHaveBeenCalled();

    controller.abort();
    await leadership;
    expect(states).toEqual(['waiting', 'leader', 'stopped']);
    expect(leaders).toEqual([false, true, false]);
  });
});
