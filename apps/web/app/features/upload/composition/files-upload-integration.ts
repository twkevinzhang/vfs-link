import {
  FilesUploadActivityAdapter,
  FilesUploadDialogAdapter,
} from '../presentation/files-upload-adapter';
import { useFilesUploadQueue } from '../presentation/upload-queue';

/** Structurally implements the Files context's consumer-owned upload port. */
export const filesUploadIntegration = {
  useQueue: useFilesUploadQueue,
  preloadDialog: () => import('../presentation/upload-dialog'),
  Activity: FilesUploadActivityAdapter,
  Dialog: FilesUploadDialogAdapter,
};
