import type { UploadFingerprint } from '../features/upload/domain/upload-queue';

export {
  duplicateLogicPaths,
  isRetryAllEligible,
  nextRunnableUploadKeys,
  retryDelayMs,
  uploadStateNeedsSource,
} from '../features/upload/domain/upload-queue';
export type { UploadFingerprint } from '../features/upload/domain/upload-queue';
export const UPLOAD_CHUNK_ALIGNMENT = 256 * 1024;
export const UPLOAD_CHUNK_SIZE = 8 * 1024 * 1024;
export const MAX_UPLOAD_CHUNK_SIZE = 128 * 1024 * 1024;
export const TARGET_UPLOAD_CHUNK_DURATION_MS = 4_000;
export const MAX_AUTOMATIC_RETRIES = 5;
export const MAX_CONCURRENT_UPLOADS = 3;

/** Browser adapter: domain fingerprints remain primitive-only. */
export function fileFingerprint(file: File): UploadFingerprint {
  return { name: file.name, size: file.size, lastModified: file.lastModified };
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

export function nextAdaptiveChunkSize(
  currentSize: number,
  uploadedBytes: number,
  elapsedMs: number
) {
  const boundedCurrent = Math.max(
    UPLOAD_CHUNK_SIZE,
    Math.min(MAX_UPLOAD_CHUNK_SIZE, currentSize)
  );
  if (
    !Number.isFinite(uploadedBytes) ||
    uploadedBytes <= 0 ||
    !Number.isFinite(elapsedMs) ||
    elapsedMs <= 0
  ) {
    return boundedCurrent;
  }
  const targetSize =
    (uploadedBytes / elapsedMs) * TARGET_UPLOAD_CHUNK_DURATION_MS;
  const growthLimited = Math.min(boundedCurrent * 4, targetSize);
  const shrinkLimited = Math.max(boundedCurrent / 2, growthLimited);
  const aligned =
    Math.floor(shrinkLimited / UPLOAD_CHUNK_ALIGNMENT) * UPLOAD_CHUNK_ALIGNMENT;
  return Math.max(UPLOAD_CHUNK_SIZE, Math.min(MAX_UPLOAD_CHUNK_SIZE, aligned));
}
