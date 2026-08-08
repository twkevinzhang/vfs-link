import type { UploadSession } from '../types/upload';
import type { UploadFingerprint } from './upload-queue-core';

const DATABASE_NAME = 'vfs-link-upload-queue';
const DATABASE_VERSION = 1;
const QUEUE_STORE = 'queue';
const SETTINGS_STORE = 'settings';

export type PersistedUploadState =
  | 'queued'
  | 'uploading'
  | 'retrying'
  | 'paused'
  | 'complete'
  | 'failed'
  | 'local-missing';

export type PersistedUploadItem = {
  key: string;
  relativePath: string;
  destinationPath: string;
  logicPath: string;
  fingerprint: UploadFingerprint;
  contentType: string;
  fileHandle?: FileSystemFileHandle;
  uploadedBytes: number;
  state: PersistedUploadState;
  error?: string;
  session?: UploadSession;
  overwrite: boolean;
  archiveGroupId?: string;
  retryCount: number;
  retryEligible: boolean;
  missingFromState?: Exclude<PersistedUploadState, 'local-missing'>;
};

function openDatabase() {
  return new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(DATABASE_NAME, DATABASE_VERSION);
    request.onupgradeneeded = () => {
      const database = request.result;
      if (!database.objectStoreNames.contains(QUEUE_STORE)) {
        database.createObjectStore(QUEUE_STORE, { keyPath: 'key' });
      }
      if (!database.objectStoreNames.contains(SETTINGS_STORE)) {
        database.createObjectStore(SETTINGS_STORE);
      }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function transactionDone(transaction: IDBTransaction) {
  return new Promise<void>((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onabort = () => reject(transaction.error);
    transaction.onerror = () => reject(transaction.error);
  });
}

export async function loadUploadQueue() {
  if (typeof indexedDB === 'undefined') {
    return { items: [] as PersistedUploadItem[], globallyPaused: false };
  }
  const database = await openDatabase();
  try {
    const transaction = database.transaction(
      [QUEUE_STORE, SETTINGS_STORE],
      'readonly'
    );
    const itemsRequest = transaction.objectStore(QUEUE_STORE).getAll();
    const pausedRequest = transaction
      .objectStore(SETTINGS_STORE)
      .get('globallyPaused');
    const result = await new Promise<{
      items: PersistedUploadItem[];
      globallyPaused: boolean;
    }>((resolve, reject) => {
      transaction.oncomplete = () =>
        resolve({
          items: itemsRequest.result as PersistedUploadItem[],
          globallyPaused: pausedRequest.result === true,
        });
      transaction.onerror = () => reject(transaction.error);
      transaction.onabort = () => reject(transaction.error);
    });
    return result;
  } finally {
    database.close();
  }
}

export async function saveUploadQueue(
  items: PersistedUploadItem[],
  globallyPaused: boolean
) {
  if (typeof indexedDB === 'undefined') return;
  const database = await openDatabase();
  try {
    const transaction = database.transaction(
      [QUEUE_STORE, SETTINGS_STORE],
      'readwrite'
    );
    const queue = transaction.objectStore(QUEUE_STORE);
    queue.clear();
    for (const item of items) queue.put(item);
    transaction
      .objectStore(SETTINGS_STORE)
      .put(globallyPaused, 'globallyPaused');
    await transactionDone(transaction);
  } finally {
    database.close();
  }
}
