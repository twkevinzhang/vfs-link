import type { UploadCancellation, UploadSession } from './upload-contracts';
import { uploadRemainingChunks } from './upload-chunks';
import type { UploadQueueDependencies } from './upload-queue-dependencies';
import {
  UploadQueueService,
  type AddUploadSource,
} from './upload-queue-service';
import { retryDelayMs, type UploadQueueItem } from '../domain/upload-queue';

class QueueCancellationController {
  private listeners = new Set<() => void>();
  private cancelled = false;

  private readonly tokenState: UploadCancellation & { aborted: boolean };
  readonly token: UploadCancellation;

  abort() {
    if (this.cancelled) return;
    this.cancelled = true;
    this.tokenState.aborted = true;
    for (const listener of this.listeners) listener();
    this.listeners.clear();
  }

  constructor() {
    this.tokenState = {
      aborted: false,
      onAbort: (listener) => {
        this.listeners.add(listener);
        return () => this.listeners.delete(listener);
      },
      throwIfAborted: () => {
        if (this.cancelled) throw new Error('Upload cancelled');
      },
    };
    this.token = this.tokenState;
  }
}

type ArchiveBatch = {
  id: string;
  sources: AddUploadSource[];
  paths: string[];
  thumbnail?: { sourceId: string; width: number; height: number };
};

type ArchiveBatchRuntime = Omit<ArchiveBatch, 'sources' | 'paths'> & {
  paths: Set<string>;
  completed: Set<string>;
};

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'Upload failed';
}

/** Executes the complete queue use case without React or browser data types. */
export class UploadQueueController {
  private readonly running = new Set<string>();
  private readonly cancellations = new Map<
    string,
    QueueCancellationController
  >();
  private readonly paused = new Set<string>();
  private readonly cancelled = new Set<string>();
  private readonly archiveBatches = new Map<string, ArchiveBatchRuntime>();
  private readonly pendingProgress = new Map<string, number>();
  private cancelProgressFrame?: () => void;
  private disposed = false;

  constructor(
    private readonly dependencies: UploadQueueDependencies,
    private readonly service = new UploadQueueService()
  ) {}

  subscribe = (listener: () => void) => this.service.subscribe(listener);
  getSnapshot = () => this.service.getSnapshot();
  subscribeStructure = (listener: () => void) =>
    this.service.subscribeStructure(listener);
  getStructureSnapshot = () => this.service.getStructureSnapshot();

  add = (sources: AddUploadSource[], destinationPath: string) => {
    const keys = this.service.add(sources, destinationPath);
    void this.runPreflight(keys);
  };

  addArchives = (batches: ArchiveBatch[], destinationPath: string) => {
    for (const batch of batches) {
      this.archiveBatches.set(batch.id, {
        id: batch.id,
        paths: new Set(batch.paths),
        completed: new Set(),
        thumbnail: batch.thumbnail,
      });
    }
    this.add(
      batches.flatMap((batch) => batch.sources),
      destinationPath
    );
  };

  private async runPreflight(keys: string[]) {
    const requested = new Set(keys);
    const candidates = this.service
      .getSnapshot()
      .items.filter(
        (item) => requested.has(item.key) && item.state === 'checking'
      );
    if (!candidates.length) return;
    try {
      const response = await this.dependencies.gateway.preflightUploads(
        candidates.map((item) => ({
          clientId: item.key,
          path: item.logicPath,
        }))
      );
      this.service.applyPreflight(requested, response.items);
      this.schedule();
    } catch (error) {
      this.service.failPreflight(
        requested,
        errorMessage(error),
        this.dependencies.errors.isTransient(error)
      );
    }
  }

  private finishArchiveItem(item: UploadQueueItem) {
    if (!item.archiveGroupId) return;
    const batch = this.archiveBatches.get(item.archiveGroupId);
    if (!batch) return;
    batch.completed.add(item.logicPath);
    if (![...batch.paths].every((path) => batch.completed.has(path))) return;
    this.archiveBatches.delete(batch.id);
    const action = batch.thumbnail
      ? this.dependencies.thumbnails.save({
          paths: [...batch.paths],
          ...batch.thumbnail,
        })
      : this.dependencies.thumbnails.clear([...batch.paths]);
    void action
      .catch(() => undefined)
      .finally(() => {
        if (batch.thumbnail) {
          this.dependencies.sources.release(batch.thumbnail.sourceId);
        }
      });
  }

  private abandonArchiveBatch(item: UploadQueueItem) {
    if (!item.archiveGroupId) return;
    const batch = this.archiveBatches.get(item.archiveGroupId);
    if (!batch) return;
    this.archiveBatches.delete(batch.id);
    if (batch.thumbnail) {
      this.dependencies.sources.release(batch.thumbnail.sourceId);
    }
  }

  private mutateAndReleaseSkipped(mutate: () => void) {
    const previousStates = new Map(
      this.service
        .getSnapshot()
        .items.map((item) => [item.key, item.state] as const)
    );
    mutate();
    for (const item of this.service.getSnapshot().items) {
      if (
        item.state !== 'skipped' ||
        previousStates.get(item.key) === 'skipped'
      ) {
        continue;
      }
      this.dependencies.sources.release(item.sourceId);
      this.abandonArchiveBatch(item);
    }
  }

  private async uploadOnce(
    key: string,
    cancellationController: QueueCancellationController
  ) {
    let item = this.service
      .getSnapshot()
      .items.find((candidate) => candidate.key === key);
    if (!item) return;
    const cancellation = cancellationController.token;
    let session = item.session;
    if (session) {
      session = await this.dependencies.gateway.getUploadSession(
        session,
        cancellation
      );
      if (session.status === 'expired') {
        this.service.markFailed(key, 'Upload session expired', false);
        return;
      }
    } else {
      session = await this.dependencies.gateway.createUpload({
        path: item.logicPath,
        size: item.fingerprint.size,
        contentType: item.contentType,
        overwrite: item.overwrite,
        targetVersion: item.overwrite ? item.targetVersion : undefined,
      });
    }
    let activeSession: UploadSession = session;
    const sourceId = item.sourceId;
    if (this.cancelled.has(key)) {
      void this.dependencies.gateway
        .cancelUpload(activeSession.id)
        .catch(() => undefined);
      return;
    }
    this.service.setSession(key, activeSession);
    let uploadedSize = activeSession.uploadedSize ?? 0;
    const total = item.fingerprint.size;
    if (total === 0 && activeSession.status !== 'uploaded') {
      uploadedSize = (
        await this.dependencies.gateway.putUploadChunk(
          activeSession,
          sourceId,
          0,
          0,
          0,
          () => undefined,
          cancellation
        )
      ).uploadedSize;
    }
    uploadedSize = await uploadRemainingChunks({
      sourceId,
      uploadedSize,
      totalSize: total,
      cancellation,
      sendChunk: (
        start,
        endExclusive,
        chunkTotal,
        onProgress,
        currentCancellation
      ) =>
        this.dependencies.gateway.putUploadChunk(
          activeSession,
          sourceId,
          start,
          endExclusive,
          chunkTotal,
          onProgress,
          currentCancellation
        ),
      reconcileOffset: async () => {
        activeSession = await this.dependencies.gateway.getUploadSession(
          activeSession,
          cancellation
        );
        return activeSession.uploadedSize ?? 0;
      },
      isOffsetConflict: this.dependencies.errors.isOffsetConflict,
      onProgress: (uploaded) => this.queueProgress(key, uploaded),
      onCommitted: (committed) => {
        activeSession = { ...activeSession, uploadedSize: committed };
        this.service.setCommitted(key, activeSession, committed);
      },
    });
    const completed = await this.dependencies.gateway.completeUpload(
      { ...activeSession, uploadedSize },
      cancellation
    );
    item = this.service
      .getSnapshot()
      .items.find((candidate) => candidate.key === key);
    if (!item || this.cancelled.has(key)) return;
    this.service.markComplete(key, completed);
    this.dependencies.sources.release(sourceId);
    this.finishArchiveItem({
      ...item,
      session: completed,
      state: 'complete',
      uploadedBytes: total,
      progress: 100,
    });
  }

  private queueProgress(key: string, uploadedBytes: number) {
    this.pendingProgress.set(key, uploadedBytes);
    if (this.cancelProgressFrame) return;
    let completedSynchronously = false;
    const cancel = this.dependencies.runtime.scheduleFrame(() => {
      completedSynchronously = true;
      this.cancelProgressFrame = undefined;
      const progress = new Map(this.pendingProgress);
      this.pendingProgress.clear();
      this.service.setProgressBatch(progress);
    });
    if (!completedSynchronously) this.cancelProgressFrame = cancel;
  }

  private async startUpload(key: string) {
    const initial = this.service
      .getSnapshot()
      .items.find((item) => item.key === key);
    if (
      !initial ||
      initial.state !== 'queued' ||
      this.service.getSnapshot().globallyPaused
    ) {
      this.running.delete(key);
      return;
    }
    const cancellation = new QueueCancellationController();
    this.cancellations.set(key, cancellation);
    this.service.markUploading(key);
    let retryCount = initial.retryCount;
    try {
      for (;;) {
        try {
          await this.uploadOnce(key, cancellation);
          return;
        } catch (error) {
          if (
            this.cancelled.has(key) ||
            this.paused.has(key) ||
            cancellation.token.aborted
          ) {
            return;
          }
          if (this.dependencies.errors.isTargetChanged(error)) {
            this.service.markTargetChanged(key);
            await this.runPreflight([key]);
            return;
          }
          if (
            this.dependencies.errors.shouldAutomaticallyRetry(error, retryCount)
          ) {
            retryCount += 1;
            const delay = retryDelayMs(retryCount);
            this.service.markRetrying(
              key,
              retryCount,
              this.dependencies.runtime.now() + delay,
              errorMessage(error)
            );
            await this.dependencies.runtime.sleep(delay, cancellation.token);
            this.service.markUploading(key);
            continue;
          }
          this.service.markFailed(
            key,
            errorMessage(error),
            this.dependencies.errors.isTransient(error),
            retryCount
          );
          return;
        }
      }
    } finally {
      this.running.delete(key);
      this.cancellations.delete(key);
      this.cancelled.delete(key);
      this.paused.delete(key);
      this.schedule();
    }
  }

  private schedule = () => {
    if (this.disposed) return;
    for (const key of this.service.nextRunnable(this.running)) {
      this.running.add(key);
      void this.startUpload(key);
    }
  };

  pause = (key: string) => {
    this.paused.add(key);
    this.cancellations.get(key)?.abort();
    this.service.pause(key);
  };

  resume = (key: string) => {
    this.paused.delete(key);
    this.service.resume(key);
    this.schedule();
  };

  pauseAll = () => {
    for (const item of this.service.getSnapshot().items) {
      if (['queued', 'uploading', 'retrying'].includes(item.state)) {
        this.paused.add(item.key);
        this.cancellations.get(item.key)?.abort();
      }
    }
    this.service.pauseAll();
  };

  resumeAll = () => {
    this.paused.clear();
    this.service.resumeAll();
    this.schedule();
  };

  cancel = (key: string) => {
    const item = this.service
      .getSnapshot()
      .items.find((candidate) => candidate.key === key);
    if (!item || item.state === 'complete') return;
    this.cancelled.add(key);
    this.cancellations.get(key)?.abort();
    if (item.session) {
      void this.dependencies.gateway
        .cancelUpload(item.session.id)
        .catch(() => undefined);
    }
    this.dependencies.sources.release(item.sourceId);
    this.abandonArchiveBatch(item);
    this.service.cancel(new Set([key]));
    this.schedule();
  };

  cancelAll = () => {
    for (const item of this.service.getSnapshot().items) {
      if (!['complete', 'skipped'].includes(item.state)) this.cancel(item.key);
    }
  };

  dismiss = (key: string) => {
    const item = this.service
      .getSnapshot()
      .items.find((candidate) => candidate.key === key);
    if (item) {
      this.dependencies.sources.release(item.sourceId);
      this.abandonArchiveBatch(item);
      if (item.session && item.state !== 'complete') {
        void this.dependencies.gateway
          .cancelUpload(item.session.id)
          .catch(() => undefined);
      }
    }
    this.service.dismiss(key);
  };

  clearFinished = () => {
    const finished = this.service
      .getSnapshot()
      .items.filter((item) =>
        ['skipped', 'complete', 'failed'].includes(item.state)
      );
    for (const item of finished) {
      this.dependencies.sources.release(item.sourceId);
      this.abandonArchiveBatch(item);
      if (
        item.state === 'failed' &&
        item.session &&
        item.session.status !== 'complete'
      ) {
        void this.dependencies.gateway
          .cancelUpload(item.session.id)
          .catch(() => undefined);
      }
    }
    this.service.clearFinished();
  };

  retry = (key: string) => {
    this.service.retry(new Set([key]));
    const item = this.service
      .getSnapshot()
      .items.find((candidate) => candidate.key === key);
    if (item?.state === 'checking') void this.runPreflight([key]);
    else this.schedule();
  };

  retryAll = () => {
    const keys = new Set(
      this.service
        .getSnapshot()
        .items.filter((item) => item.state === 'failed' && item.retryEligible)
        .map((item) => item.key)
    );
    this.service.retry(keys);
    const preflight = this.service
      .getSnapshot()
      .items.filter((item) => keys.has(item.key) && item.state === 'checking')
      .map((item) => item.key);
    if (preflight.length) void this.runPreflight(preflight);
    this.schedule();
  };

  replaceOne = (key: string) => {
    this.mutateAndReleaseSkipped(() => this.service.replaceOne(key));
    this.schedule();
  };
  skipOne = (key: string) => {
    this.mutateAndReleaseSkipped(() => this.service.skipOne(key));
  };
  replaceAll = (batchId: string) => {
    this.service.replaceAll(batchId);
    this.schedule();
  };
  skipAll = (batchId: string) => {
    this.mutateAndReleaseSkipped(() => this.service.skipAll(batchId));
  };

  dispose() {
    this.disposed = true;
    this.cancelProgressFrame?.();
    this.cancelProgressFrame = undefined;
    this.pendingProgress.clear();
    for (const cancellation of this.cancellations.values()) {
      cancellation.abort();
    }
    this.cancellations.clear();
    this.archiveBatches.clear();
    this.dependencies.sources.clear();
  }
}
