export type UploadQueueState =
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

export type UploadFingerprint = {
  name: string;
  size: number;
  lastModified: number;
};

export type UploadQueueProjection = {
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

export function uploadStateNeedsSource(state: string) {
  return !['complete', 'skipped', 'local-missing'].includes(state);
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
