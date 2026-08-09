import type { ShareRecord } from '../domain/share';

export type ShareRequestCancellation = {
  readonly cancelled: boolean;
  onCancel(listener: () => void): () => void;
};

export type ShareGateway = {
  createShareDraft(path: string): Promise<ShareRecord>;
  getShare(
    id: string,
    cancellation?: ShareRequestCancellation
  ): Promise<ShareRecord>;
  startShare(
    id: string,
    cancellation?: ShareRequestCancellation
  ): Promise<ShareRecord>;
};
