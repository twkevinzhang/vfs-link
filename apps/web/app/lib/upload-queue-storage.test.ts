import 'fake-indexeddb/auto';

import { beforeEach, describe, expect, it } from 'vitest';

import type { PersistedUploadItem } from './upload-queue-storage';
import { loadUploadQueue, saveUploadQueue } from './upload-queue-storage';

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

function pendingItem(overrides: Partial<PersistedUploadItem> = {}) {
  return {
    key: 'upload-1',
    relativePath: 'source.txt',
    destinationPath: '',
    logicPath: 'source.txt',
    fingerprint: {
      name: 'source.txt',
      size: 12,
      lastModified: 123,
    },
    contentType: 'text/plain',
    uploadedBytes: 5,
    state: 'uploading',
    overwrite: false,
    retryCount: 0,
    retryEligible: true,
    ...overrides,
  } satisfies PersistedUploadItem;
}

describe('upload queue persistence', () => {
  beforeEach(async () => {
    await deleteDatabase();
  });

  it('restores the source snapshot after a page refresh', async () => {
    const item = pendingItem();
    const file = new File(['hello world!'], 'source.txt', {
      lastModified: 123,
      type: 'text/plain',
    });

    await saveUploadQueue([item], false, [
      { key: item.key, file, retainFile: true },
    ]);

    const restored = await loadUploadQueue();

    expect(restored.globallyPaused).toBe(false);
    expect(restored.items).toHaveLength(1);
    expect(restored.items[0]).toMatchObject({
      key: item.key,
      state: 'uploading',
      uploadedBytes: 5,
    });
    expect(await restored.items[0].sourceFile?.text()).toBe('hello world!');
  });

  it('does not rewrite or discard the snapshot on progress updates', async () => {
    const item = pendingItem();
    const file = new File(['hello world!'], 'source.txt', {
      lastModified: 123,
    });

    await saveUploadQueue([item], false, [
      { key: item.key, file, retainFile: true },
    ]);
    await saveUploadQueue([{ ...item, uploadedBytes: 9 }], false, [
      { key: item.key, file, retainFile: true },
    ]);

    const restored = await loadUploadQueue();
    expect(restored.items[0].uploadedBytes).toBe(9);
    expect(await restored.items[0].sourceFile?.text()).toBe('hello world!');
  });

  it('removes the source snapshot after the upload completes', async () => {
    const item = pendingItem();
    const file = new File(['hello world!'], 'source.txt', {
      lastModified: 123,
    });

    await saveUploadQueue([item], false, [
      { key: item.key, file, retainFile: true },
    ]);
    await saveUploadQueue(
      [{ ...item, state: 'complete', uploadedBytes: item.fingerprint.size }],
      false,
      [{ key: item.key, file, retainFile: false }]
    );

    const restored = await loadUploadQueue();
    expect(restored.items[0].state).toBe('complete');
    expect(restored.items[0].sourceFile).toBeUndefined();
  });
});
