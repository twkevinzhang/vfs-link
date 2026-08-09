import type { ShareRecord } from '../domain/share';

export type ShareGateway = {
  createShareDraft(path: string): Promise<ShareRecord>;
  getShare(
    id: string,
    options?: { signal?: AbortSignal }
  ): Promise<ShareRecord>;
  startShare(
    id: string,
    options?: { signal?: AbortSignal }
  ): Promise<ShareRecord>;
};
