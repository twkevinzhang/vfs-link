import type {
  CreateUploadInput,
  UploadPreflightItemInput,
  UploadPreflightResponse,
  UploadSession,
} from './upload-contracts';

export type UploadChunkResult = { uploadedSize: number; status: number };

export type UploadGateway = {
  createUpload(input: CreateUploadInput): Promise<UploadSession>;
  preflightUploads(
    items: UploadPreflightItemInput[]
  ): Promise<UploadPreflightResponse>;
  getUploadSession(
    session: Pick<UploadSession, 'statusUrl'>,
    signal?: AbortSignal
  ): Promise<UploadSession>;
  completeUpload(
    session: UploadSession,
    signal?: AbortSignal
  ): Promise<UploadSession>;
  cancelUpload(id: string): Promise<void>;
  putUploadChunk(
    session: UploadSession,
    chunk: Blob,
    start: number,
    total: number,
    onProgress: (uploaded: number, total: number) => void,
    signal?: AbortSignal
  ): Promise<UploadChunkResult>;
};
