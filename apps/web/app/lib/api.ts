/**
 * @deprecated Compatibility facade for tests and incremental migration only.
 * Production callers import the owning context gateway directly.
 */
export {
  createThumbnail,
  deleteThumbnails,
  deleteTrash,
  emptyTrash,
  getDownloadUrl,
  getFileOperation,
  getFiles,
  getPreviewUrl,
  getStatus,
  getThumbnailUrl,
  getTrash,
  getTree,
  moveFiles,
  moveFilesToTrash,
  renameFile,
  restoreTrash,
} from '../features/files/infrastructure/files-http-gateway';
export type { GetFilesOptions } from '../features/files/application/files-gateway';

export {
  createShareDraft,
  getShare,
  startShare,
} from '../features/share/infrastructure/share-http-gateway';

export {
  cancelUpload,
  committedOffsetFromRange,
  completeUpload,
  createUpload,
  getUploadSession,
  preflightUploads,
  putUploadChunk,
  UploadHttpError,
} from '../features/upload/infrastructure/upload-http-gateway';
export type { UploadChunkResult } from '../features/upload/application/upload-gateway';

export {
  createDriftAction,
  createDriftPlan,
  dismissDriftAction,
  getCurrentDriftScan,
  getDrift,
  getDriftAction,
  getDriftActions,
  startDriftScan,
} from '../features/drift/infrastructure/drift-http-gateway';
export type { GetDriftOptions } from '../features/drift/application/drift-gateway';
