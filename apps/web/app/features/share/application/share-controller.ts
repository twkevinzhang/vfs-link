import type { ShareRecord } from '../domain/share';
import type { ShareGateway } from './share-gateway';
import {
  createShareRequestCoordinator,
  settleShareRequest,
  type ShareDeadlineScheduler,
  type ShareRequestCoordinator,
} from './share-request-coordinator';

const SHARE_POLL_MS = 1_500;

export type ShareScheduler = ShareDeadlineScheduler & {
  setInterval(callback: () => void, intervalMs: number): unknown;
  clearInterval(handle: unknown): void;
};

export type ShareControllerSnapshot = {
  share?: ShareRecord;
  loading: boolean;
  starting: boolean;
  error?: string;
  refresh(): Promise<ShareRecord | undefined>;
  startShare(): Promise<ShareRecord | undefined>;
};

export class ShareController {
  private listeners = new Set<() => void>();
  private snapshot: ShareControllerSnapshot;
  private coordinator?: ShareRequestCoordinator;
  private pollInterval?: unknown;
  private active = false;
  private startGeneration = 0;
  private share?: ShareRecord;
  private loading = true;
  private starting = false;
  private error?: string;

  constructor(
    private readonly shareId: string | undefined,
    private readonly gateway: ShareGateway,
    private readonly scheduler: ShareScheduler
  ) {
    this.snapshot = this.buildSnapshot();
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = () => this.snapshot;

  start = () => {
    if (this.active) return;
    this.active = true;
    if (!this.shareId) {
      this.loading = false;
      this.error = 'Missing share id';
      this.emit();
      return;
    }
    this.coordinator = createShareRequestCoordinator({
      scheduler: this.scheduler,
      load: (cancellation) =>
        this.gateway.getShare(this.shareId as string, cancellation),
      start: (cancellation) =>
        this.gateway.startShare(this.shareId as string, cancellation),
      onSuccess: (share) => {
        if (!this.active) return;
        this.share = share;
        this.error = undefined;
        this.loading = false;
        this.emit();
      },
      onError: (error) => {
        if (!this.active) return;
        this.error =
          error instanceof Error ? error.message : 'Unable to load share';
        this.loading = false;
        this.emit();
      },
    });
    void settleShareRequest(this.coordinator.refresh());
    this.pollInterval = this.scheduler.setInterval(() => {
      if (!this.coordinator) return;
      void settleShareRequest(this.coordinator.poll());
    }, SHARE_POLL_MS);
  };

  dispose = () => {
    this.active = false;
    this.startGeneration += 1;
    this.coordinator?.dispose();
    this.coordinator = undefined;
    if (this.pollInterval !== undefined) {
      this.scheduler.clearInterval(this.pollInterval);
      this.pollInterval = undefined;
    }
  };

  refresh = async () => {
    if (!this.coordinator) return undefined;
    return settleShareRequest(this.coordinator.refresh());
  };

  startShare = async () => {
    if (!this.coordinator || this.starting) return undefined;
    const generation = ++this.startGeneration;
    this.starting = true;
    this.error = undefined;
    this.emit();
    try {
      return await this.coordinator.start();
    } catch {
      return undefined;
    } finally {
      if (this.active && generation === this.startGeneration) {
        this.starting = false;
        this.emit();
      }
    }
  };

  private emit() {
    this.snapshot = this.buildSnapshot();
    for (const listener of this.listeners) listener();
  }

  private buildSnapshot(): ShareControllerSnapshot {
    return {
      share: this.share,
      loading: this.loading,
      starting: this.starting,
      error: this.error,
      refresh: this.refresh,
      startShare: this.startShare,
    };
  }
}
