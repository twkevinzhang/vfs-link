import axios from 'axios';

/**
 * Download file with resumable support
 * Uses Range requests to support pause/resume
 */
export async function downloadFile(
  signedUrl: string,
  filename: string,
  onProgress?: (downloadedBytes: number) => void,
  signal?: AbortSignal,
  startByte = 0
): Promise<void> {
  // Download with Range header for resumable support
  const headers: Record<string, string> = {};
  if (startByte > 0) {
    headers['Range'] = `bytes=${startByte}-`;
  }

  const response = await axios.get(signedUrl, {
    responseType: 'blob',
    signal,
    headers,
    onDownloadProgress: (progressEvent) => {
      if (onProgress && progressEvent.loaded) {
        // Add startByte to show total progress
        onProgress(startByte + progressEvent.loaded);
      }
    },
  });

  const blob = response.data;
  const blobUrl = URL.createObjectURL(blob);

  try {
    const a = document.createElement('a');
    a.href = blobUrl;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  } finally {
    // Cleanup blob URL after a delay to ensure download starts
    setTimeout(() => URL.revokeObjectURL(blobUrl), 1000);
  }
}
