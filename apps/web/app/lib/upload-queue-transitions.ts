import type { UploadQueueItem, UploadQueueState } from './upload-queue-model';
import { uploadProgress } from './upload-queue-runtime';

const pausableStates = new Set<UploadQueueState>([
  'queued',
  'uploading',
  'retrying',
]);

export function pauseUploadQueueItem(item: UploadQueueItem): UploadQueueItem {
  if (!pausableStates.has(item.state)) return item;
  return {
    ...item,
    state: 'paused',
    retryAt: undefined,
    error: undefined,
    progress: uploadProgress(item.uploadedBytes, item.fingerprint.size),
  };
}

export function resumeUploadQueueItem(item: UploadQueueItem): UploadQueueItem {
  if (item.state !== 'paused' || (!item.file && !item.fileHandle)) return item;
  return {
    ...item,
    state: 'queued',
    error: undefined,
  };
}

export function cancelUploadQueueItems(
  items: UploadQueueItem[],
  keys: ReadonlySet<string>
): UploadQueueItem[] {
  return items.filter((item) => !keys.has(item.key));
}
