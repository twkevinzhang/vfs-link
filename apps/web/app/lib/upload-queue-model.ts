import type { ArchiveTemporaryManifest } from './archive-compression';
import type { UploadFingerprint } from './upload-queue-core';
import type { PersistedUploadState } from './upload-queue-storage';
import type {
  UploadPreflightExisting,
  UploadPreflightStatus,
  UploadSession,
} from '../types/upload';

export type UploadQueueState = PersistedUploadState;

export type UploadQueueItem = {
  key: string;
  /** One user selection event; bulk conflict actions never cross this boundary. */
  batchId: string;
  file?: File;
  fileHandle?: FileSystemFileHandle;
  fingerprint: UploadFingerprint;
  contentType: string;
  relativePath: string;
  /** The folder selected when the item was added, rather than the current view. */
  destinationPath: string;
  /** Fully resolved storage path, also captured when the item was added. */
  logicPath: string;
  uploadedBytes: number;
  progress: number;
  state: UploadQueueState;
  error?: string;
  session?: UploadSession;
  /** Whether a direct child with this name existed when the item was added. */
  overwrite: boolean;
  /** Opaque target version captured by the most recent server preflight. */
  targetVersion?: string;
  targetStatus?: UploadPreflightStatus;
  existingTarget?: UploadPreflightExisting;
  /** Multiple source files in this batch resolve to the same logical path. */
  localDuplicate: boolean;
  archiveGroupId?: string;
  archiveTemporaryManifest?: ArchiveTemporaryManifest;
  retryCount: number;
  retryEligible: boolean;
  retryAt?: number;
  missingFromState?: Exclude<UploadQueueState, 'local-missing'>;
};

export type UploadQueueSummary = {
  total: number;
  queued: number;
  checking: number;
  needsDecision: number;
  skipped: number;
  uploading: number;
  retrying: number;
  paused: number;
  complete: number;
  failed: number;
  localMissing: number;
  totalBytes: number;
  uploadedBytes: number;
  /** Byte-weighted progress across every retained queue item. */
  progress: number;
};
