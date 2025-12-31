import { createXXHash64, createCRC32 } from 'hash-wasm';

export interface HashResult {
  xxHash64: string;
  crc32c: string;
}

export async function calculateHashes(
  file: File,
  onProgress?: (progress: number) => void
): Promise<HashResult> {
  const xxHasher = await createXXHash64();
  const crcHasher = await createCRC32();

  const totalBytes = file.size;
  let processedBytes = 0;

  const stream = file.stream();
  const reader = stream.getReader();

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      xxHasher.update(value);
      crcHasher.update(value);

      processedBytes += value.byteLength;
      if (onProgress) {
        onProgress((processedBytes / totalBytes) * 100);
      }
    }

    return {
      xxHash64: xxHasher.digest('hex'),
      crc32c: crcHasher.digest('hex'),
    };
  } finally {
    reader.releaseLock();
  }
}

export async function calculateCRC32C(
  file: File,
  onProgress?: (progress: number) => void
): Promise<string> {
  const hasher = await createCRC32();

  const totalBytes = file.size;
  let processedBytes = 0;

  const stream = file.stream();
  const reader = stream.getReader();

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      hasher.update(value);

      processedBytes += value.byteLength;
      if (onProgress) {
        onProgress((processedBytes / totalBytes) * 100);
      }
    }

    return hasher.digest('hex');
  } finally {
    reader.releaseLock();
  }
}
