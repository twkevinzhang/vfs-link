import { afterEach, describe, expect, it, vi } from 'vitest';

import type { UploadQueueItem } from './upload-queue-model';
import {
  restoreUploadQueueItem,
  summarizeUploadQueue,
  toPersistedUploadItem,
  waitForUploadRetry,
} from './upload-queue-runtime';
import { inspectUploadSource } from './upload-queue-source';
import type { PersistedUploadItem } from './upload-queue-storage';

function persistedItem(
  overrides: Partial<PersistedUploadItem> = {}
): PersistedUploadItem {
  return {
    key: 'upload-1',
    batchId: 'batch-1',
    relativePath: 'source.txt',
    destinationPath: '',
    logicPath: 'source.txt',
    fingerprint: { name: 'source.txt', size: 3, lastModified: 123 },
    contentType: 'text/plain',
    uploadedBytes: 1,
    state: 'uploading',
    overwrite: false,
    localDuplicate: false,
    retryCount: 1,
    retryEligible: true,
    ...overrides,
  };
}

function queueItem(overrides: Partial<UploadQueueItem> = {}): UploadQueueItem {
  return {
    ...persistedItem(),
    file: new File(['abc'], 'source.txt', { lastModified: 123 }),
    progress: 100 / 3,
    retryAt: 456,
    ...overrides,
  };
}

function fileHandle({
  permission = 'granted',
  file = new File(['abc'], 'source.txt', { lastModified: 123 }),
  queryError,
  fileError,
}: {
  permission?: PermissionState;
  file?: File;
  queryError?: Error;
  fileError?: Error;
} = {}) {
  return {
    kind: 'file',
    name: file.name,
    queryPermission: vi.fn(async () => {
      if (queryError) throw queryError;
      return permission;
    }),
    getFile: vi.fn(async () => {
      if (fileError) throw fileError;
      return file;
    }),
  } as unknown as FileSystemFileHandle;
}

afterEach(() => {
  vi.useRealTimers();
});

describe('upload retry wait', () => {
  it('resolves after the requested delay', async () => {
    vi.useFakeTimers();
    const promise = waitForUploadRetry(500, new AbortController().signal);

    await vi.advanceTimersByTimeAsync(499);
    let settled = false;
    void promise.then(() => {
      settled = true;
    });
    await Promise.resolve();
    expect(settled).toBe(false);

    await vi.advanceTimersByTimeAsync(1);
    await expect(promise).resolves.toBeUndefined();
  });

  it('rejects a pending or already-aborted wait without leaving a timer', async () => {
    vi.useFakeTimers();
    const pendingController = new AbortController();
    const pending = waitForUploadRetry(500, pendingController.signal);
    pendingController.abort();
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
    expect(vi.getTimerCount()).toBe(0);

    const staleController = new AbortController();
    staleController.abort();
    await expect(
      waitForUploadRetry(500, staleController.signal)
    ).rejects.toMatchObject({ name: 'AbortError' });
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe('upload source inspection and restore', () => {
  it('reads a granted source handle', async () => {
    const handle = fileHandle();

    await expect(inspectUploadSource(handle)).resolves.toMatchObject({
      status: 'available',
      file: { name: 'source.txt', size: 3, lastModified: 123 },
    });
  });

  it('distinguishes permission-required and missing sources', async () => {
    await expect(
      inspectUploadSource(fileHandle({ permission: 'prompt' }))
    ).resolves.toEqual({ status: 'permission-required' });
    await expect(
      inspectUploadSource(fileHandle({ permission: 'denied' }))
    ).resolves.toEqual({ status: 'missing' });
    await expect(
      inspectUploadSource(
        fileHandle({
          queryError: new DOMException('blocked', 'NotAllowedError'),
        })
      )
    ).resolves.toEqual({ status: 'permission-required' });
    await expect(
      inspectUploadSource(
        fileHandle({ fileError: new DOMException('gone', 'NotFoundError') })
      )
    ).resolves.toEqual({ status: 'missing' });
  });

  it('pauses restored work when source permission must be renewed', async () => {
    const restored = await restoreUploadQueueItem(
      persistedItem({
        fileHandle: fileHandle({ permission: 'prompt' }),
        targetStatus: 'available',
      }),
      false
    );

    expect(restored).toMatchObject({
      state: 'paused',
      error: '請允許讀取原始檔案以繼續上傳。',
    });
    expect(restored.progress).toBeCloseTo(100 / 3);
  });

  it('marks a missing or changed restored source as local-missing', async () => {
    const restored = await restoreUploadQueueItem(
      persistedItem({
        fileHandle: fileHandle({
          file: new File(['changed'], 'source.txt', { lastModified: 123 }),
        }),
      }),
      false
    );

    expect(restored).toMatchObject({
      state: 'local-missing',
      missingFromState: 'uploading',
      error: '已從本機移除',
    });
  });

  it('re-checks stale runnable work without a server preflight or session', async () => {
    const restored = await restoreUploadQueueItem(
      persistedItem({ fileHandle: fileHandle() }),
      false
    );

    expect(restored.state).toBe('checking');
    expect(restored.file).toBeInstanceOf(File);
  });
});

describe('upload queue runtime mapping', () => {
  it('projects only persisted fields from a runtime queue item', () => {
    const runtime = queueItem();
    const persisted = toPersistedUploadItem(runtime);

    expect(persisted).toMatchObject({
      key: 'upload-1',
      state: 'uploading',
      uploadedBytes: 1,
      retryCount: 1,
    });
    expect(persisted).not.toHaveProperty('file');
    expect(persisted).not.toHaveProperty('progress');
    expect(persisted).not.toHaveProperty('retryAt');
  });

  it('summarizes byte progress and treats skipped work as finished', () => {
    const summary = summarizeUploadQueue([
      queueItem({ key: 'active', uploadedBytes: 1 }),
      queueItem({
        key: 'skipped',
        state: 'skipped',
        uploadedBytes: 0,
        progress: 0,
      }),
    ]);

    expect(summary).toMatchObject({
      total: 2,
      uploading: 1,
      skipped: 1,
      totalBytes: 6,
      uploadedBytes: 1,
      progress: (4 / 6) * 100,
    });
  });
});
