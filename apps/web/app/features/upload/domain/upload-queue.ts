export type UploadQueueState =
  | 'queued'
  | 'checking'
  | 'needs-decision'
  | 'skipped'
  | 'uploading'
  | 'retrying'
  | 'paused'
  | 'complete'
  | 'failed';

export type UploadFingerprint = {
  name: string;
  size: number;
  lastModified: number;
};

export type UploadSession = {
  id: string;
  status:
    | 'pending'
    | 'uploading'
    | 'uploaded'
    | 'complete'
    | 'failed'
    | 'expired';
  uploadedSize: number;
  error?: string;
  expiresAt: string;
};

export type UploadPreflightStatus = 'available' | 'conflict' | 'directory';
export type UploadPreflightExisting = {
  kind: 'file' | 'directory';
  size: number;
  updatedAt: string;
};

export type UploadQueueRecord = {
  key: string;
  batchId: string;
  fingerprint: UploadFingerprint;
  relativePath: string;
  destinationPath: string;
  logicPath: string;
  uploadedBytes: number;
  state: UploadQueueState;
  retryCount: number;
  retryEligible: boolean;
};

export type UploadQueueItem = UploadQueueRecord & {
  sourceId: string;
  contentType: string;
  progress: number;
  error?: string;
  session?: UploadSession;
  overwrite: boolean;
  targetVersion?: string;
  targetStatus?: UploadPreflightStatus;
  existingTarget?: UploadPreflightExisting;
  localDuplicate: boolean;
  archiveGroupId?: string;
  retryAt?: number;
};

export type UploadQueueSummary = {
  total: number;
  queued: number;
  checking: number;
  needsDecision: number;
  skipped: number;
  uploading: number;
  retrying: number;
  paused: number;
  complete: number;
  failed: number;
  totalBytes: number;
  uploadedBytes: number;
  progress: number;
};

export type UploadFailure = {
  kind: 'network' | 'timeout' | 'rate-limit' | 'server' | 'conflict' | 'other';
  code?: string;
};

export const MAX_AUTOMATIC_RETRIES = 5;

export function isTransientUploadFailure(failure: UploadFailure) {
  return ['network', 'timeout', 'rate-limit', 'server'].includes(failure.kind);
}

export function shouldAutomaticallyRetryFailure(
  failure: UploadFailure,
  retriesAlreadyUsed: number
) {
  return (
    isTransientUploadFailure(failure) &&
    retriesAlreadyUsed < MAX_AUTOMATIC_RETRIES
  );
}

export function uploadProgress(uploadedBytes: number, size: number) {
  return size === 0
    ? uploadedBytes === 0
      ? 0
      : 100
    : (uploadedBytes / size) * 100;
}

export function summarizeUploadQueue(
  items: readonly UploadQueueItem[]
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
    totalBytes: 0,
    uploadedBytes: 0,
    progress: 0,
  };
  let progressBytes = 0;
  for (const item of items) {
    if (item.state === 'needs-decision') counts.needsDecision += 1;
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
  if (item.state !== 'paused') return item;
  return { ...item, state: 'queued', error: undefined };
}

export function duplicateLogicPaths<T extends { logicPath: string }>(
  items: T[]
) {
  const counts = new Map<string, number>();
  for (const item of items) {
    counts.set(item.logicPath, (counts.get(item.logicPath) ?? 0) + 1);
  }
  return new Set(
    [...counts.entries()]
      .filter(([, count]) => count > 1)
      .map(([logicPath]) => logicPath)
  );
}

export function nextRunnableUploadKeys<
  T extends { key: string; state: string }
>(items: T[], runningKeys: ReadonlySet<string>, limit: number) {
  if (limit <= 0) return [];
  return items
    .filter((item) => item.state === 'queued' && !runningKeys.has(item.key))
    .slice(0, limit)
    .map((item) => item.key);
}

export function isRetryAllEligible(
  state: string,
  retryEligible: boolean,
  sourceAvailable: boolean
) {
  return state === 'failed' && retryEligible && sourceAvailable;
}

/** retryNumber is 1-based: first retry waits about 500 ms. */
export function retryDelayMs(retryNumber: number, random = Math.random) {
  const base = Math.min(15_000, 500 * 2 ** Math.max(0, retryNumber - 1));
  return Math.round(base * (0.75 + random() * 0.5));
}
