import type { UploadCandidate } from './upload-candidates';

const MAX_EDGE = 512;

export async function createArchiveThumbnail(
  candidate: UploadCandidate,
  signal?: AbortSignal
) {
  signal?.throwIfAborted();
  const bitmap = await createImageBitmap(candidate.file);
  try {
    signal?.throwIfAborted();
    const scale = Math.min(1, MAX_EDGE / Math.max(bitmap.width, bitmap.height));
    const width = Math.max(1, Math.round(bitmap.width * scale));
    const height = Math.max(1, Math.round(bitmap.height * scale));
    const canvas = document.createElement('canvas');
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext('2d');
    if (!context) throw new Error('無法建立縮圖繪圖環境');
    context.drawImage(bitmap, 0, 0, width, height);
    const blob = await new Promise<Blob | null>((resolve) =>
      canvas.toBlob(resolve, 'image/webp', 0.82)
    );
    signal?.throwIfAborted();
    if (!blob) throw new Error('瀏覽器無法輸出 WebP 縮圖');
    return { blob, width, height };
  } finally {
    bitmap.close();
  }
}

export async function firstDecodableThumbnail(
  candidates: UploadCandidate[],
  signal?: AbortSignal
) {
  for (const candidate of candidates) {
    signal?.throwIfAborted();
    try {
      return {
        candidate,
        thumbnail: await createArchiveThumbnail(candidate, signal),
      };
    } catch {
      signal?.throwIfAborted();
      /* corrupt images do not block archive creation */
    }
  }
  return undefined;
}
