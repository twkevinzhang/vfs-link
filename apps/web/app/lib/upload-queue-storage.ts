import type { UploadSession } from '../types/upload';
import type { UploadFingerprint } from './upload-queue-core';

const DATABASE_NAME = 'vfs-link-upload-queue';
const DATABASE_VERSION = 2;
const QUEUE_STORE = 'queue';
const SETTINGS_STORE = 'settings';
const SOURCE_STORE = 'sources';

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

export type PersistedUploadSource = {
  key: string;
  file?: File;
  fileHandle?: FileSystemFileHandle;
};

export type UploadSourceSnapshot = PersistedUploadSource & {
  retainFile: boolean;
};

export type LoadedUploadItem = PersistedUploadItem & {
  sourceFile?: File;
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
      if (!database.objectStoreNames.contains(SOURCE_STORE)) {
        database.createObjectStore(SOURCE_STORE, { keyPath: 'key' });
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
    return { items: [] as LoadedUploadItem[], globallyPaused: false };
  }
  const database = await openDatabase();
  try {
    const transaction = database.transaction(
      [QUEUE_STORE, SETTINGS_STORE, SOURCE_STORE],
      'readonly'
    );
    const itemsRequest = transaction.objectStore(QUEUE_STORE).getAll();
    const sourcesRequest = transaction.objectStore(SOURCE_STORE).getAll();
    const pausedRequest = transaction
      .objectStore(SETTINGS_STORE)
      .get('globallyPaused');
    const result = await new Promise<{
      items: LoadedUploadItem[];
      globallyPaused: boolean;
    }>((resolve, reject) => {
      transaction.oncomplete = () =>
        resolve({
          items: (() => {
            const sources = new Map(
              (sourcesRequest.result as PersistedUploadSource[]).map(
                (source) => [source.key, source] as const
              )
            );
            return (itemsRequest.result as PersistedUploadItem[]).map(
              (item): LoadedUploadItem => {
                const source = sources.get(item.key);
                return {
                  ...item,
                  fileHandle: source?.fileHandle ?? item.fileHandle,
                  sourceFile: source?.file,
                };
              }
            );
          })(),
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
  globallyPaused: boolean,
  sourceSnapshots: UploadSourceSnapshot[] = []
) {
  if (typeof indexedDB === 'undefined') return;
  const database = await openDatabase();
  try {
    const transaction = database.transaction(
      [QUEUE_STORE, SETTINGS_STORE, SOURCE_STORE],
      'readwrite'
    );
    const queue = transaction.objectStore(QUEUE_STORE);
    const sources = transaction.objectStore(SOURCE_STORE);
    queue.clear();
    for (const item of items) queue.put(item);
    const retainedKeys = new Set(items.map((item) => item.key));
    const sourceKeysRequest = sources.getAllKeys();
    sourceKeysRequest.onsuccess = () => {
      for (const key of sourceKeysRequest.result) {
        if (typeof key === 'string' && !retainedKeys.has(key)) {
          sources.delete(key);
        }
      }
    };
    for (const snapshot of sourceSnapshots) {
      const sourceRequest = sources.get(snapshot.key);
      sourceRequest.onsuccess = () => {
        const existing = sourceRequest.result as
          | PersistedUploadSource
          | undefined;
        const addingFile =
          snapshot.retainFile && !existing?.file && Boolean(snapshot.file);
        const removingFile = !snapshot.retainFile && Boolean(existing?.file);
        const file = snapshot.retainFile
          ? existing?.file ?? snapshot.file
          : undefined;
        const fileHandle = addingFile
          ? snapshot.fileHandle ?? existing?.fileHandle
          : existing?.fileHandle ?? snapshot.fileHandle;
        if (!file && !fileHandle) {
          sources.delete(snapshot.key);
          return;
        }
        if (existing && !addingFile && !removingFile) {
          return;
        }
        sources.put({ key: snapshot.key, file, fileHandle });
      };
    }
    transaction
      .objectStore(SETTINGS_STORE)
      .put(globallyPaused, 'globallyPaused');
    await transactionDone(transaction);
  } finally {
    database.close();
  }
}
