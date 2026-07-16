export type UploadSession = {
  id: string;
  logicPath: string;
  size: number;
  contentType: string;
  status: 'pending' | 'uploading' | 'uploaded' | 'complete' | 'failed';
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
};
