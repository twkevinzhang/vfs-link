import type {
  CreateUploadInput,
  UploadPreflightItemInput,
  UploadPreflightResponse,
  UploadSession,
  UploadCancellation,
} from './upload-contracts';

export type UploadChunkResult = { uploadedSize: number; status: number };

export type UploadGateway = {
  createUpload(input: CreateUploadInput): Promise<UploadSession>;
  preflightUploads(
    items: UploadPreflightItemInput[]
  ): Promise<UploadPreflightResponse>;
  getUploadSession(
    session: Pick<UploadSession, 'id'>,
    cancellation?: UploadCancellation
  ): Promise<UploadSession>;
  completeUpload(
    session: UploadSession,
    cancellation?: UploadCancellation
  ): Promise<UploadSession>;
  cancelUpload(id: string): Promise<void>;
  putUploadChunk(
    session: UploadSession,
    sourceId: string,
    start: number,
    endExclusive: number,
    total: number,
    onProgress: (uploaded: number, total: number) => void,
    cancellation?: UploadCancellation
  ): Promise<UploadChunkResult>;
};
