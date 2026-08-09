import type { FilesControllerScheduler } from '../application/files-controller';

export const browserFilesScheduler: FilesControllerScheduler = {
  schedule(task, delayMs) {
    const timer = globalThis.setTimeout(task, delayMs);
    return () => globalThis.clearTimeout(timer);
  },
};
