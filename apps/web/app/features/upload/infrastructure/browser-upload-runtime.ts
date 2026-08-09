import type { UploadCancellation } from '../application/upload-contracts';

export const browserUploadRuntime = {
  now: Date.now,
  scheduleFrame(callback: () => void) {
    const frame = window.requestAnimationFrame(callback);
    return () => window.cancelAnimationFrame(frame);
  },
  sleep(delayMs: number, cancellation: UploadCancellation) {
    return new Promise<void>((resolve, reject) => {
      if (cancellation.aborted) {
        reject(new Error('Upload cancelled'));
        return;
      }
      const timer = window.setTimeout(resolve, delayMs);
      cancellation.onAbort(() => {
        window.clearTimeout(timer);
        reject(new Error('Upload cancelled'));
      });
    });
  },
};
