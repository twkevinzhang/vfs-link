import {
  matchesFingerprint,
  uploadStateNeedsSource,
} from './upload-queue-core';
import type { UploadQueueItem, UploadQueueSummary } from './upload-queue-model';
import { inspectUploadSource } from './upload-queue-source';
import type { PersistedUploadItem } from './upload-queue-storage';

export function uploadErrorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'Upload failed';
}

export function uploadProgress(uploadedBytes: number, size: number) {
  return size === 0
    ? uploadedBytes === 0
      ? 0
      : 100
    : (uploadedBytes / size) * 100;
}

/** Projects runtime queue state onto the explicitly persisted schema. */
export function toPersistedUploadItem(
  item: UploadQueueItem
): PersistedUploadItem {
  return {
    key: item.key,
    batchId: item.batchId,
    relativePath: item.relativePath,
    destinationPath: item.destinationPath,
    logicPath: item.logicPath,
    fingerprint: item.fingerprint,
    contentType: item.contentType,
    fileHandle: item.fileHandle,
    uploadedBytes: item.uploadedBytes,
    state: item.state,
    error: item.error,
    session: item.session,
    overwrite: item.overwrite,
    targetVersion: item.targetVersion,
    targetStatus: item.targetStatus,
    existingTarget: item.existingTarget,
    localDuplicate: item.localDuplicate,
    archiveGroupId: item.archiveGroupId,
    archiveTemporaryManifest: item.archiveTemporaryManifest,
    retryCount: item.retryCount,
    retryEligible: item.retryEligible,
    missingFromState: item.missingFromState,
  };
}

function uploadAbortError() {
  return new DOMException('Upload paused', 'AbortError');
}

export function waitForUploadRetry(delay: number, signal: AbortSignal) {
  if (signal.aborted) return Promise.reject(uploadAbortError());
  return new Promise<void>((resolve, reject) => {
    const onAbort = () => {
      globalThis.clearTimeout(timer);
      reject(uploadAbortError());
    };
    const timer = globalThis.setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, delay);
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

export async function restoreUploadQueueItem(
  stored: PersistedUploadItem,
  globallyPaused: boolean
): Promise<UploadQueueItem> {
  let file: File | undefined;
  let sourceMissing = false;
  let permissionRequired = false;
  if (stored.fileHandle) {
    try {
      const result = await inspectUploadSource(stored.fileHandle);
      if (result.status === 'available') {
        if (matchesFingerprint(result.file, stored.fingerprint)) {
          file = result.file;
        } else {
          sourceMissing = true;
        }
      } else if (result.status === 'missing') {
        sourceMissing = true;
      } else {
        permissionRequired = true;
      }
    } catch {
      sourceMissing = true;
    }
  }

  let state = stored.state;
  let error = stored.error;
  let missingFromState = stored.missingFromState;
  if (sourceMissing) {
    missingFromState =
      stored.state === 'local-missing' ? stored.missingFromState : stored.state;
    state = 'local-missing';
    error = '已從本機移除';
  } else if (
    !file &&
    !stored.fileHandle &&
    uploadStateNeedsSource(stored.state)
  ) {
    state = 'paused';
    error = '請重新選擇原始檔案以繼續上傳。';
  } else if (
    !file &&
    permissionRequired &&
    uploadStateNeedsSource(stored.state)
  ) {
    state = 'paused';
    error = '請允許讀取原始檔案以繼續上傳。';
  } else if (
    !stored.session &&
    !stored.targetStatus &&
    ['queued', 'uploading', 'retrying'].includes(stored.state)
  ) {
    state = 'checking';
    error = undefined;
  } else if (['queued', 'uploading', 'retrying'].includes(stored.state)) {
    state = globallyPaused ? 'paused' : 'queued';
  }

  return {
    ...stored,
    batchId: stored.batchId || stored.key,
    localDuplicate: stored.localDuplicate ?? false,
    file,
    state,
    error,
    missingFromState,
    progress: uploadProgress(stored.uploadedBytes, stored.fingerprint.size),
  };
}

export function summarizeUploadQueue(
  items: UploadQueueItem[]
): UploadQueueSummary {
  const counts: UploadQueueSummary = {
    total: items.length,
    queued: 0,
    checking: 0,
    needsDecision: 0,
    skipped: 0,
    uploading: 0,
    retrying: 0,
    paused: 0,
    complete: 0,
    failed: 0,
    localMissing: 0,
    totalBytes: 0,
    uploadedBytes: 0,
    progress: 0,
  };
  let progressBytes = 0;
  for (const item of items) {
    if (item.state === 'local-missing') counts.localMissing += 1;
    else if (item.state === 'needs-decision') counts.needsDecision += 1;
    else counts[item.state] += 1;
    counts.totalBytes += item.fingerprint.size;
    counts.uploadedBytes += Math.min(item.fingerprint.size, item.uploadedBytes);
    progressBytes +=
      item.state === 'skipped'
        ? item.fingerprint.size
        : Math.min(item.fingerprint.size, item.uploadedBytes);
  }
  counts.progress =
    counts.totalBytes > 0
      ? Math.min(100, (progressBytes / counts.totalBytes) * 100)
      : counts.total > 0
      ? ((counts.complete + counts.skipped) / counts.total) * 100
      : 0;
  return counts;
}
