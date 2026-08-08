export type UploadSession = {
  id: string;
  logicPath: string;
  size: number;
  contentType: string;
  status:
    | 'pending'
    | 'uploading'
    | 'uploaded'
    | 'complete'
    | 'failed'
    | 'expired';
  /** Bytes durably committed by the storage backend. */
  uploadedSize: number;
  error?: string;
  method: 'PUT';
  uploadUrl: string;
  headers: Record<string, string>;
  completeUrl: string;
  statusUrl: string;
  expiresAt: string;
};

export type CreateUploadInput = {
  path: string;
  size: number;
  contentType: string;
  overwrite: boolean;
  /** Opaque version returned by preflight when overwriting an existing path. */
  targetVersion?: string;
};

export type UploadPreflightStatus = 'available' | 'conflict' | 'directory';

export type UploadPreflightItemInput = {
  clientId: string;
  path: string;
};

export type UploadPreflightExisting = {
  kind: 'file' | 'directory';
  size: number;
  updatedAt: string;
};

export type UploadPreflightItem = UploadPreflightItemInput & {
  status: UploadPreflightStatus;
  existing?: UploadPreflightExisting;
  /** Required by createUpload when the user decides to overwrite. */
  targetVersion: string;
};

export type UploadPreflightResponse = {
  items: UploadPreflightItem[];
};
