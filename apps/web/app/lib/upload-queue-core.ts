import { UploadHttpError } from './api';

export const UPLOAD_CHUNK_SIZE = 8 * 1024 * 1024;
export const MAX_AUTOMATIC_RETRIES = 5;
export const MAX_CONCURRENT_UPLOADS = 3;

export type UploadFingerprint = {
  name: string;
  size: number;
  lastModified: number;
};

export function fileFingerprint(file: File): UploadFingerprint {
  return {
    name: file.name,
    size: file.size,
    lastModified: file.lastModified,
  };
}

export function matchesFingerprint(file: File, fingerprint: UploadFingerprint) {
  return (
    file.name === fingerprint.name &&
    file.size === fingerprint.size &&
    file.lastModified === fingerprint.lastModified
  );
}

export function nextChunkRange(
  uploadedSize: number,
  totalSize: number,
  chunkSize = UPLOAD_CHUNK_SIZE
) {
  const start = Math.max(0, Math.min(uploadedSize, totalSize));
  return { start, endExclusive: Math.min(totalSize, start + chunkSize) };
}

export function isTransientUploadError(error: unknown) {
  // Browser fetch rejects with TypeError when the request never receives an
  // HTTP response (offline, DNS failure, connection reset). XHR paths are
  // normalized to UploadHttpError, but create/status/complete use fetch and
  // must receive the same retry policy.
  if (error instanceof TypeError) return true;
  if (!(error instanceof UploadHttpError)) return false;
  if (error.status === undefined || error.status === 0) return true;
  return (
    error.status === 408 ||
    error.status === 425 ||
    error.status === 429 ||
    error.status >= 500
  );
}

export function isOffsetConflict(error: unknown) {
  return error instanceof UploadHttpError && error.status === 409;
}

export function shouldAutomaticallyRetry(
  error: unknown,
  retriesAlreadyUsed: number
) {
  return (
    isTransientUploadError(error) && retriesAlreadyUsed < MAX_AUTOMATIC_RETRIES
  );
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
