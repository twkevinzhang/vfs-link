import type {
  UploadPreflightExisting,
  UploadPreflightStatus,
} from '../domain/upload-queue';

export type {
  UploadPreflightExisting,
  UploadPreflightStatus,
  UploadSession,
} from '../domain/upload-queue';

export type CreateUploadInput = {
  path: string;
  size: number;
  contentType: string;
  overwrite: boolean;
  targetVersion?: string;
};

export type UploadPreflightItemInput = { clientId: string; path: string };
export type UploadPreflightItem = UploadPreflightItemInput & {
  status: UploadPreflightStatus;
  existing?: UploadPreflightExisting;
  targetVersion: string;
};
export type UploadPreflightResponse = { items: UploadPreflightItem[] };

/** Primitive-only description registered by the browser source adapter. */
export type UploadSourceDescriptor = {
  sourceId: string;
  name: string;
  size: number;
  lastModified: number;
  contentType: string;
};

export type UploadCancellation = {
  readonly aborted: boolean;
  onAbort(listener: () => void): () => void;
  throwIfAborted(): void;
};
