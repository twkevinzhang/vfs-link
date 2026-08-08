import 'fake-indexeddb/auto';

import { beforeEach, describe, expect, it } from 'vitest';

import type { UploadSession } from '../types/upload';
import type { PersistedUploadItem } from './upload-queue-storage';
import {
  clearUploadQueueStorage,
  getUploadQueueStorageStatus,
  loadUploadQueue,
  saveUploadQueue,
  subscribeUploadQueueStorageStatus,
} from './upload-queue-storage';

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

function openDatabase(version?: number) {
  return new Promise<IDBDatabase>((resolve, reject) => {
    const request =
      version === undefined
        ? indexedDB.open(DATABASE_NAME)
        : indexedDB.open(DATABASE_NAME, version);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function transactionDone(transaction: IDBTransaction) {
  return new Promise<void>((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error);
    transaction.onabort = () => reject(transaction.error);
  });
}

function pendingItem(overrides: Partial<PersistedUploadItem> = {}) {
  return {
    key: 'upload-1',
    batchId: 'batch-1',
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
    targetVersion: 'target-v1',
    targetStatus: 'conflict',
    existingTarget: {
      kind: 'file',
      size: 8,
      updatedAt: '2026-08-08T00:00:00Z',
    },
    localDuplicate: false,
    retryCount: 0,
    retryEligible: true,
    ...overrides,
  } satisfies PersistedUploadItem;
}

function uploadSession(): UploadSession {
  return {
    id: 'session-1',
    logicPath: 'source.txt',
    size: 12,
    contentType: 'text/plain',
    status: 'uploading',
    uploadedSize: 5,
    method: 'PUT',
    uploadUrl: '/api/uploads/session-1/content',
    headers: { 'Content-Type': 'text/plain' },
    completeUrl: '/api/uploads/session-1/complete',
    statusUrl: '/api/uploads/session-1',
    expiresAt: '2026-08-09T12:00:00Z',
  };
}

async function createVersion3Database(item: PersistedUploadItem) {
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(DATABASE_NAME, 3);
    request.onupgradeneeded = () => {
      request.result.createObjectStore('queue', { keyPath: 'key' });
      request.result.createObjectStore('settings');
      request.result.createObjectStore('sources', { keyPath: 'key' });
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  const transaction = database.transaction(
    ['queue', 'settings', 'sources'],
    'readwrite'
  );
  transaction.objectStore('queue').put(item);
  transaction.objectStore('settings').put(true, 'globallyPaused');
  transaction.objectStore('sources').put({
    key: item.key,
    file: new File(['legacy snapshot'], 'source.txt'),
  });
  await transactionDone(transaction);
  database.close();
}

function containsFileOrBlob(value: unknown, seen = new Set<object>()): boolean {
  if (!value || typeof value !== 'object') return false;
  if (value instanceof Blob) return true;
  if (seen.has(value)) return false;
  seen.add(value);
  return Object.values(value).some((child) => containsFileOrBlob(child, seen));
}

describe('upload queue persistence v4', () => {
  beforeEach(async () => {
    await deleteDatabase();
  });

  it('migrates v3 in place, preserves queue metadata, and deletes sources', async () => {
    const item = pendingItem({ session: uploadSession() });
    await createVersion3Database(item);

    const restored = await loadUploadQueue();

    expect(restored).toMatchObject({
      globallyPaused: true,
      migratedLegacySources: true,
      items: [
        {
          key: item.key,
          batchId: 'batch-1',
          state: 'uploading',
          uploadedBytes: 5,
          targetVersion: 'target-v1',
          session: { id: 'session-1', uploadedSize: 5 },
        },
      ],
    });
    expect(restored.items[0]).not.toHaveProperty('sourceFile');
    expect(restored.items[0]).not.toHaveProperty('file');
    expect(getUploadQueueStorageStatus()).toEqual({
      state: 'ready',
      migratedLegacySources: true,
    });

    const database = await openDatabase();
    expect(database.version).toBe(4);
    expect([...database.objectStoreNames]).toEqual(['queue', 'settings']);
    database.close();
  });

  it('is idempotent across repeated v4 opens and saves', async () => {
    const item = pendingItem();
    await createVersion3Database(item);

    await loadUploadQueue();
    await saveUploadQueue([{ ...item, uploadedBytes: 9 }], false);
    const first = await loadUploadQueue();
    const second = await loadUploadQueue();

    expect(first).toEqual(second);
    expect(second.items).toHaveLength(1);
    expect(second.items[0].uploadedBytes).toBe(9);
    expect(first.migratedLegacySources).toBe(false);
    expect(second.migratedLegacySources).toBe(false);
    const database = await openDatabase();
    expect(database.version).toBe(4);
    expect(database.objectStoreNames.contains('sources')).toBe(false);
    database.close();
  });

  it('never persists File or Blob values from runtime-polluted input', async () => {
    const fakeHandle = {
      kind: 'file',
      name: 'source.txt',
    } as FileSystemFileHandle;
    const item = Object.assign(
      pendingItem({ fileHandle: fakeHandle, session: uploadSession() }),
      {
        file: new File(['runtime only'], 'source.txt'),
        sourceFile: new File(['runtime only'], 'source.txt'),
        blob: new Blob(['runtime only']),
        archiveTemporaryManifest: {
          version: 1,
          ownerId: 'archive-1',
          createdAt: 123,
          files: [
            {
              name: 'archive-part.zip',
              size: 12,
              snapshot: new File(['runtime only'], 'archive-part.zip'),
            },
          ],
        },
      }
    );
    const session = item.session;
    if (!session) throw new Error('Test upload session unavailable');
    Object.assign(session, { snapshot: new Blob(['nested']) });

    await saveUploadQueue([item], false);
    const restored = await loadUploadQueue();
    const database = await openDatabase();
    const transaction = database.transaction('queue', 'readonly');
    const rawRequest = transaction.objectStore('queue').get(item.key);
    await transactionDone(transaction);
    const raw = rawRequest.result as Record<string, unknown>;
    database.close();

    expect(containsFileOrBlob(raw)).toBe(false);
    expect(containsFileOrBlob(restored)).toBe(false);
    expect(raw).not.toHaveProperty('file');
    expect(raw).not.toHaveProperty('sourceFile');
    expect(raw).not.toHaveProperty('blob');
    expect(raw.session).not.toHaveProperty('snapshot');
    expect(raw.fileHandle).toEqual(fakeHandle);
    expect(raw.archiveTemporaryManifest).toEqual({
      version: 1,
      ownerId: 'archive-1',
      createdAt: 123,
      files: [{ name: 'archive-part.zip', size: 12 }],
    });
  });

  it('publishes opening and ready states to observers', async () => {
    const states: string[] = [];
    const unsubscribe = subscribeUploadQueueStorageStatus((status) =>
      states.push(status.state)
    );

    await loadUploadQueue();
    unsubscribe();

    expect(states).toContain('opening');
    expect(states).toContain('ready');
    expect(getUploadQueueStorageStatus().state).toBe('ready');
  });

  it('atomically clears queue and settings after the transaction completes', async () => {
    await saveUploadQueue([pendingItem()], true);

    await clearUploadQueueStorage();
    const restored = await loadUploadQueue();

    expect(restored.items).toEqual([]);
    expect(restored.globallyPaused).toBe(false);
  });

  it('reports blocked upgrades and closes the eventual late connection', async () => {
    await createVersion3Database(pendingItem());
    const blocker = await openDatabase(3);
    const states: string[] = [];
    const unsubscribe = subscribeUploadQueueStorageStatus((status) =>
      states.push(status.state)
    );

    await expect(clearUploadQueueStorage()).rejects.toThrow('blocked');
    expect(states).toContain('blocked');
    expect(getUploadQueueStorageStatus().state).toBe('blocked');

    blocker.close();
    unsubscribe();
    await new Promise((resolve) => setTimeout(resolve, 0));
    const restored = await loadUploadQueue();
    expect(restored.items).toHaveLength(1);
  });

  it('reports VersionError when a newer database already exists', async () => {
    const newer = await openDatabase(5);
    newer.close();

    await expect(loadUploadQueue()).rejects.toMatchObject({
      name: 'VersionError',
    });
    expect(getUploadQueueStorageStatus().state).toBe('version-error');
  });
});
