import type { UploadChunkResult } from './api';
import {
  UPLOAD_CHUNK_SIZE,
  isOffsetConflict,
  nextAdaptiveChunkSize,
  nextChunkRange,
} from './upload-queue-core';

type UploadFileSource = Pick<Blob, 'slice'>;

type UploadRemainingChunksOptions = {
  file: UploadFileSource;
  uploadedSize: number;
  totalSize: number;
  contentType: string;
  signal: AbortSignal;
  sendChunk: (
    chunk: Blob,
    start: number,
    total: number,
    onProgress: (uploaded: number, total: number) => void,
    signal: AbortSignal
  ) => Promise<UploadChunkResult>;
  reconcileOffset: () => Promise<number>;
  onProgress: (uploaded: number, total: number) => void;
  onCommitted: (uploadedSize: number) => void;
  now?: () => number;
};

/** Uploads every remaining byte while treating the server offset as truth. */
export async function uploadRemainingChunks({
  file,
  uploadedSize: initialUploadedSize,
  totalSize,
  contentType,
  signal,
  sendChunk,
  reconcileOffset,
  onProgress,
  onCommitted,
  now = () => performance.now(),
}: UploadRemainingChunksOptions) {
  let uploadedSize = initialUploadedSize;
  let chunkSize = UPLOAD_CHUNK_SIZE;

  while (uploadedSize < totalSize) {
    signal.throwIfAborted();
    const { start, endExclusive } = nextChunkRange(
      uploadedSize,
      totalSize,
      chunkSize
    );
    try {
      const startedAt = now();
      const result = await sendChunk(
        file.slice(start, endExclusive, contentType),
        start,
        totalSize,
        onProgress,
        signal
      );
      uploadedSize = result.uploadedSize;
      chunkSize = nextAdaptiveChunkSize(
        chunkSize,
        Math.max(0, Math.min(endExclusive, uploadedSize) - start),
        now() - startedAt
      );
    } catch (error) {
      if (!isOffsetConflict(error)) throw error;
      uploadedSize = Math.max(0, Math.min(totalSize, await reconcileOffset()));
    }
    onCommitted(uploadedSize);
  }

  return uploadedSize;
}
