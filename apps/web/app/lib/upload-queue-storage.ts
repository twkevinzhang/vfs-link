import type { UploadSession } from '../types/upload';
import type { ArchiveTemporaryManifest } from './archive-compression';
import type { UploadFingerprint } from './upload-queue-core';

const DATABASE_NAME = 'vfs-link-upload-queue';
const DATABASE_VERSION = 4;
const QUEUE_STORE = 'queue';
const SETTINGS_STORE = 'settings';

export type PersistedUploadState =
  | 'queued'
  | 'checking'
  | 'needs-decision'
  | 'skipped'
  | 'uploading'
  | 'retrying'
  | 'paused'
  | 'complete'
  | 'failed'
  | 'local-missing';

export type PersistedUploadItem = {
  key: string;
  batchId: string;
  relativePath: string;
  destinationPath: string;
  logicPath: string;
  fingerprint: UploadFingerprint;
  contentType: string;
  /** Opaque browser handle only. File and Blob snapshots are never stored. */
  fileHandle?: FileSystemFileHandle;
  uploadedBytes: number;
  state: PersistedUploadState;
  error?: string;
  session?: UploadSession;
  overwrite: boolean;
  targetVersion?: string;
  targetStatus?: 'available' | 'conflict' | 'directory';
  existingTarget?: {
    kind: 'file' | 'directory';
    size: number;
    updatedAt: string;
  };
  localDuplicate: boolean;
  archiveGroupId?: string;
  archiveTemporaryManifest?: ArchiveTemporaryManifest;
  retryCount: number;
  retryEligible: boolean;
  missingFromState?: Exclude<PersistedUploadState, 'local-missing'>;
};

function isFileOrBlob(value: unknown) {
  if (!value || typeof value !== 'object') return false;
  if (typeof Blob !== 'undefined' && value instanceof Blob) return true;
  const tag = Object.prototype.toString.call(value);
  return tag === '[object Blob]' || tag === '[object File]';
}

function containsFileOrBlob(value: unknown, seen = new Set<object>()): boolean {
  if (isFileOrBlob(value)) return true;
  if (!value || typeof value !== 'object' || seen.has(value)) return false;
  seen.add(value);
  for (const child of Object.values(value)) {
    if (containsFileOrBlob(child, seen)) return true;
  }
  return false;
}

function safeFileHandle(value: unknown) {
  if (
    !value ||
    typeof value !== 'object' ||
    containsFileOrBlob(value) ||
    !('kind' in value) ||
    value.kind !== 'file' ||
    !('name' in value) ||
    typeof value.name !== 'string'
  ) {
    return undefined;
  }
  return value as FileSystemFileHandle;
}

function sanitizeSession(session: UploadSession | undefined) {
  if (!session) return undefined;
  const headers = Object.fromEntries(
    Object.entries(session.headers ?? {}).filter(
      (entry): entry is [string, string] => typeof entry[1] === 'string'
    )
  );
  return {
    id: session.id,
    logicPath: session.logicPath,
    size: session.size,
    contentType: session.contentType,
    status: session.status,
    uploadedSize: session.uploadedSize,
    error: session.error,
    method: session.method,
    uploadUrl: session.uploadUrl,
    headers,
    completeUrl: session.completeUrl,
    statusUrl: session.statusUrl,
    expiresAt: session.expiresAt,
  } satisfies UploadSession;
}

function sanitizeArchiveTemporaryManifest(value: unknown) {
  if (!value || typeof value !== 'object') return undefined;
  const manifest = value as Partial<ArchiveTemporaryManifest>;
  if (
    manifest.version !== 1 ||
    typeof manifest.ownerId !== 'string' ||
    typeof manifest.createdAt !== 'number' ||
    !Number.isFinite(manifest.createdAt) ||
    !Array.isArray(manifest.files)
  ) {
    return undefined;
  }
  const files = manifest.files.flatMap((file) =>
    file &&
    typeof file === 'object' &&
    typeof file.name === 'string' &&
    typeof file.size === 'number' &&
    Number.isFinite(file.size) &&
    file.size >= 0
      ? [{ name: file.name, size: file.size }]
      : []
  );
  if (files.length !== manifest.files.length) return undefined;
  return {
    version: 1,
    ownerId: manifest.ownerId,
    createdAt: manifest.createdAt,
    files,
  } satisfies ArchiveTemporaryManifest;
}

/**
 * Explicit projection is the persistence boundary. Runtime-only File/Blob
 * properties and future unknown fields cannot leak into IndexedDB.
 */
function sanitizePersistedItem(item: PersistedUploadItem) {
  const fileHandle = safeFileHandle(item.fileHandle);
  return {
    key: item.key,
    batchId: item.batchId,
    relativePath: item.relativePath,
    destinationPath: item.destinationPath,
    logicPath: item.logicPath,
    fingerprint: {
      name: item.fingerprint.name,
      size: item.fingerprint.size,
      lastModified: item.fingerprint.lastModified,
    },
    contentType: item.contentType,
    ...(fileHandle ? { fileHandle } : {}),
    uploadedBytes: item.uploadedBytes,
    state: item.state,
    error: item.error,
    session: sanitizeSession(item.session),
    overwrite: item.overwrite,
    targetVersion: item.targetVersion,
    targetStatus: item.targetStatus,
    existingTarget: item.existingTarget
      ? {
          kind: item.existingTarget.kind,
          size: item.existingTarget.size,
          updatedAt: item.existingTarget.updatedAt,
        }
      : undefined,
    localDuplicate: item.localDuplicate,
    archiveGroupId: item.archiveGroupId,
    archiveTemporaryManifest: sanitizeArchiveTemporaryManifest(
      item.archiveTemporaryManifest
    ),
    retryCount: item.retryCount,
    retryEligible: item.retryEligible,
    missingFromState: item.missingFromState,
  } satisfies PersistedUploadItem;
}

function blockedError() {
  return new Error(
    'Upload queue database upgrade is blocked by another open tab.'
  );
}

function openDatabase() {
  return new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(DATABASE_NAME, DATABASE_VERSION);
    let settled = false;

    request.onupgradeneeded = () => {
      const database = request.result;
      if (!database.objectStoreNames.contains(QUEUE_STORE)) {
        database.createObjectStore(QUEUE_STORE, { keyPath: 'key' });
      }
      if (!database.objectStoreNames.contains(SETTINGS_STORE)) {
        database.createObjectStore(SETTINGS_STORE);
      }
    };

    request.onblocked = () => {
      const error = blockedError();
      if (!settled) {
        settled = true;
        reject(error);
      }
    };

    request.onsuccess = () => {
      const database = request.result;
      if (settled) {
        database.close();
        return;
      }
      settled = true;
      database.onversionchange = () => database.close();
      resolve(database);
    };

    request.onerror = () => {
      if (settled) return;
      settled = true;
      const error = request.error ?? new Error('Upload queue database failed');
      reject(error);
    };
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
    return {
      items: [] as PersistedUploadItem[],
      globallyPaused: false,
    };
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
          items: (itemsRequest.result as PersistedUploadItem[]).map(
            sanitizePersistedItem
          ),
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
    for (const item of items) queue.put(sanitizePersistedItem(item));
    transaction
      .objectStore(SETTINGS_STORE)
      .put(globallyPaused, 'globallyPaused');
    await transactionDone(transaction);
  } finally {
    database.close();
  }
}
