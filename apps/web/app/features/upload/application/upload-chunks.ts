import type { UploadCancellation } from './upload-contracts';
import type { UploadChunkResult } from './upload-gateway';

export const UPLOAD_CHUNK_ALIGNMENT = 256 * 1024;
export const UPLOAD_CHUNK_SIZE = 8 * 1024 * 1024;
export const MAX_UPLOAD_CHUNK_SIZE = 128 * 1024 * 1024;
export const TARGET_UPLOAD_CHUNK_DURATION_MS = 4_000;

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
  const bounded = Math.max(
    UPLOAD_CHUNK_SIZE,
    Math.min(MAX_UPLOAD_CHUNK_SIZE, currentSize)
  );
  if (
    !Number.isFinite(uploadedBytes) ||
    uploadedBytes <= 0 ||
    !Number.isFinite(elapsedMs) ||
    elapsedMs <= 0
  )
    return bounded;
  const target = (uploadedBytes / elapsedMs) * TARGET_UPLOAD_CHUNK_DURATION_MS;
  const limited = Math.max(bounded / 2, Math.min(bounded * 4, target));
  const aligned =
    Math.floor(limited / UPLOAD_CHUNK_ALIGNMENT) * UPLOAD_CHUNK_ALIGNMENT;
  return Math.max(UPLOAD_CHUNK_SIZE, Math.min(MAX_UPLOAD_CHUNK_SIZE, aligned));
}

export async function uploadRemainingChunks(options: {
  sourceId: string;
  uploadedSize: number;
  totalSize: number;
  cancellation: UploadCancellation;
  sendChunk(
    start: number,
    endExclusive: number,
    total: number,
    onProgress: (uploaded: number, total: number) => void,
    cancellation: UploadCancellation
  ): Promise<UploadChunkResult>;
  reconcileOffset(): Promise<number>;
  isOffsetConflict(error: unknown): boolean;
  onProgress(uploaded: number, total: number): void;
  onCommitted(uploadedSize: number): void;
  now?: () => number;
}) {
  let uploadedSize = options.uploadedSize;
  let chunkSize = UPLOAD_CHUNK_SIZE;
  const now = options.now ?? Date.now;
  while (uploadedSize < options.totalSize) {
    options.cancellation.throwIfAborted();
    const { start, endExclusive } = nextChunkRange(
      uploadedSize,
      options.totalSize,
      chunkSize
    );
    try {
      const startedAt = now();
      const result = await options.sendChunk(
        start,
        endExclusive,
        options.totalSize,
        options.onProgress,
        options.cancellation
      );
      uploadedSize = result.uploadedSize;
      chunkSize = nextAdaptiveChunkSize(
        chunkSize,
        Math.max(0, Math.min(endExclusive, uploadedSize) - start),
        now() - startedAt
      );
    } catch (error) {
      if (!options.isOffsetConflict(error)) throw error;
      uploadedSize = Math.max(
        0,
        Math.min(options.totalSize, await options.reconcileOffset())
      );
    }
    options.onCommitted(uploadedSize);
  }
  return uploadedSize;
}
