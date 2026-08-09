import type {
  UploadPreflightItem,
  UploadSourceDescriptor,
} from './upload-contracts';
import {
  duplicateLogicPaths,
  isRetryAllEligible,
  nextRunnableUploadKeys,
  pauseUploadQueueItem,
  resumeUploadQueueItem,
  summarizeUploadQueue,
  uploadProgress,
  type UploadQueueItem,
  type UploadQueueSummary,
  type UploadSession,
} from '../domain/upload-queue';

export const MAX_CONCURRENT_UPLOADS = 3;

export type AddUploadSource = UploadSourceDescriptor & {
  relativePath: string;
  archiveGroupId?: string;
};

export type UploadQueueSnapshot = {
  items: readonly UploadQueueItem[];
  globallyPaused: boolean;
  hasPendingUploads: boolean;
  summary: UploadQueueSummary;
};

export type UploadQueueStructureSnapshot = {
  items: ReadonlyArray<
    Pick<UploadQueueItem, 'key' | 'destinationPath' | 'state'>
  >;
  summary: Pick<
    UploadQueueSummary,
    'checking' | 'queued' | 'uploading' | 'retrying' | 'paused'
  >;
};

function normalizePath(value: string) {
  return value
    .replaceAll('\\', '/')
    .split('/')
    .filter((part) => part && part !== '.' && part !== '..')
    .join('/');
}

let keySequence = 0;
function randomKey(prefix: string) {
  keySequence += 1;
  return `${prefix}-${Date.now()}-${keySequence}-${Math.random()
    .toString(36)
    .slice(2)}`;
}

/**
 * Session-scoped upload state machine. It owns all business transitions and
 * deliberately has no persistence or browser source types.
 */
export class UploadQueueService {
  private items: UploadQueueItem[] = [];
  private readonly itemIndex = new Map<string, number>();
  private globallyPaused = false;
  private readonly listeners = new Set<() => void>();
  private readonly structureListeners = new Set<() => void>();
  private snapshot: UploadQueueSnapshot;
  private structureSnapshot: UploadQueueStructureSnapshot;
  private summary: UploadQueueSummary;
  private progressBytes = 0;

  constructor(
    private readonly summarize: typeof summarizeUploadQueue = summarizeUploadQueue,
    private readonly onItemsCopied: () => void = () => undefined
  ) {
    this.summary = this.summarize(this.items);
    this.snapshot = this.createSnapshot();
    this.structureSnapshot = this.createStructureSnapshot();
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  subscribeStructure = (listener: () => void) => {
    this.structureListeners.add(listener);
    return () => this.structureListeners.delete(listener);
  };

  private createSnapshot = (): UploadQueueSnapshot => ({
    items: this.items,
    globallyPaused: this.globallyPaused,
    hasPendingUploads:
      this.summary.checking +
        this.summary.queued +
        this.summary.uploading +
        this.summary.retrying >
      0,
    summary: this.summary,
  });

  getSnapshot = () => this.snapshot;

  private createStructureSnapshot = (): UploadQueueStructureSnapshot => ({
    items: this.items.map(({ key, destinationPath, state }) => ({
      key,
      destinationPath,
      state,
    })),
    summary: {
      checking: this.summary.checking,
      queued: this.summary.queued,
      uploading: this.summary.uploading,
      retrying: this.summary.retrying,
      paused: this.summary.paused,
    },
  });

  getStructureSnapshot = () => this.structureSnapshot;

  private notifyStructure() {
    this.structureSnapshot = this.createStructureSnapshot();
    for (const listener of this.structureListeners) listener();
  }

  private publish(next: UploadQueueItem[]) {
    this.items = next;
    this.itemIndex.clear();
    this.progressBytes = 0;
    for (const [index, item] of this.items.entries()) {
      this.itemIndex.set(item.key, index);
      this.progressBytes += this.progressContribution(item);
    }
    this.summary = this.summarize(this.items);
    this.snapshot = this.createSnapshot();
    this.notifyStructure();
    for (const listener of this.listeners) listener();
  }

  private progressContribution(item: UploadQueueItem) {
    return item.state === 'skipped'
      ? item.fingerprint.size
      : Math.min(item.fingerprint.size, item.uploadedBytes);
  }

  private summaryStateKey(state: UploadQueueItem['state']) {
    return state === 'needs-decision' ? 'needsDecision' : state;
  }

  private publishItem(
    index: number,
    previous: UploadQueueItem,
    next: UploadQueueItem
  ) {
    if (previous === next) return;
    const items = this.items.slice();
    this.onItemsCopied();
    items[index] = next;
    this.items = items;
    this.progressBytes +=
      this.progressContribution(next) - this.progressContribution(previous);
    const summary = { ...this.summary };
    if (previous.state !== next.state) {
      summary[this.summaryStateKey(previous.state)] -= 1;
      summary[this.summaryStateKey(next.state)] += 1;
    }
    summary.uploadedBytes +=
      Math.min(next.fingerprint.size, next.uploadedBytes) -
      Math.min(previous.fingerprint.size, previous.uploadedBytes);
    summary.progress =
      summary.totalBytes > 0
        ? Math.min(100, (this.progressBytes / summary.totalBytes) * 100)
        : summary.total > 0
        ? ((summary.complete + summary.skipped) / summary.total) * 100
        : 0;
    this.summary = summary;
    this.snapshot = this.createSnapshot();
    if (previous.state !== next.state) this.notifyStructure();
    for (const listener of this.listeners) listener();
  }

  update(key: string, update: (item: UploadQueueItem) => UploadQueueItem) {
    const index = this.itemIndex.get(key);
    if (index === undefined) return;
    const previous = this.items[index];
    this.publishItem(index, previous, update(previous));
  }

  add(sources: readonly AddUploadSource[], destinationPath: string) {
    const destination = normalizePath(destinationPath);
    const batchId = randomKey('batch');
    const drafts = sources.map((source) => ({
      source,
      logicPath: normalizePath(`${destination}/${source.relativePath}`),
    }));
    const duplicates = duplicateLogicPaths(drafts);
    const additions = drafts.map(
      ({ source, logicPath }): UploadQueueItem => ({
        key: randomKey(source.relativePath),
        batchId,
        sourceId: source.sourceId,
        fingerprint: {
          name: source.name,
          size: source.size,
          lastModified: source.lastModified,
        },
        contentType: source.contentType,
        relativePath: source.relativePath,
        destinationPath: destination,
        logicPath,
        uploadedBytes: 0,
        progress: 0,
        state: 'checking',
        overwrite: false,
        localDuplicate: duplicates.has(logicPath),
        archiveGroupId: source.archiveGroupId,
        retryCount: 0,
        retryEligible: false,
      })
    );
    if (additions.length) this.publish([...this.items, ...additions]);
    return additions.map((item) => item.key);
  }

  applyPreflight(
    keys: ReadonlySet<string>,
    results: readonly UploadPreflightItem[]
  ) {
    const byKey = new Map(results.map((result) => [result.clientId, result]));
    this.publish(
      this.items.map((item) => {
        if (!keys.has(item.key) || item.state !== 'checking') return item;
        const result = byKey.get(item.key);
        if (!result) {
          return {
            ...item,
            state: 'failed',
            retryEligible: true,
            error: 'Preflight response did not include this upload.',
          };
        }
        const needsDecision =
          item.localDuplicate || result.status !== 'available';
        return {
          ...item,
          targetStatus: result.status,
          targetVersion: result.targetVersion,
          existingTarget: result.existing,
          overwrite: false,
          state: needsDecision
            ? 'needs-decision'
            : this.globallyPaused
            ? 'paused'
            : 'queued',
          retryCount: 0,
          retryEligible: false,
          error:
            result.status === 'directory'
              ? '目的路徑是資料夾，無法用檔案取代。'
              : undefined,
        };
      })
    );
  }

  failPreflight(
    keys: ReadonlySet<string>,
    error: string,
    retryEligible: boolean
  ) {
    this.publish(
      this.items.map((item) =>
        keys.has(item.key) && item.state === 'checking'
          ? { ...item, state: 'failed', retryEligible, error }
          : item
      )
    );
  }

  replaceOne(key: string) {
    const selected = this.items.find((item) => item.key === key);
    if (
      !selected ||
      selected.state !== 'needs-decision' ||
      selected.targetStatus === 'directory'
    )
      return;
    this.publish(
      this.items.map((item) => {
        if (
          selected.localDuplicate &&
          item.key !== selected.key &&
          item.batchId === selected.batchId &&
          item.logicPath === selected.logicPath &&
          item.state === 'needs-decision'
        )
          return {
            ...item,
            state: 'skipped',
            retryEligible: false,
            error: undefined,
          };
        if (item.key !== key) return item;
        return {
          ...item,
          overwrite: item.targetStatus === 'conflict',
          state: this.globallyPaused ? 'paused' : 'queued',
          retryEligible: false,
          error: undefined,
        };
      })
    );
  }

  skipOne(key: string) {
    this.update(key, (item) =>
      item.state === 'needs-decision'
        ? { ...item, state: 'skipped', retryEligible: false, error: undefined }
        : item
    );
  }

  replaceAll(batchId: string) {
    this.publish(
      this.items.map((item) =>
        item.batchId === batchId &&
        item.state === 'needs-decision' &&
        !item.localDuplicate &&
        item.targetStatus !== 'directory'
          ? {
              ...item,
              overwrite: item.targetStatus === 'conflict',
              state: this.globallyPaused ? 'paused' : 'queued',
              retryEligible: false,
              error: undefined,
            }
          : item
      )
    );
  }

  skipAll(batchId: string) {
    this.publish(
      this.items.map((item) =>
        item.batchId === batchId && item.state === 'needs-decision'
          ? {
              ...item,
              state: 'skipped',
              retryEligible: false,
              error: undefined,
            }
          : item
      )
    );
  }

  pause(key: string) {
    this.update(key, pauseUploadQueueItem);
  }
  resume(key: string) {
    this.update(key, resumeUploadQueueItem);
  }

  pauseAll() {
    this.globallyPaused = true;
    this.publish(this.items.map(pauseUploadQueueItem));
  }

  resumeAll() {
    this.globallyPaused = false;
    this.publish(this.items.map(resumeUploadQueueItem));
  }

  cancel(keys: ReadonlySet<string>) {
    this.publish(this.items.filter((item) => !keys.has(item.key)));
  }

  dismiss(key: string) {
    const item = this.items.find((candidate) => candidate.key === key);
    if (
      !item ||
      ['checking', 'queued', 'uploading', 'retrying'].includes(item.state)
    )
      return;
    this.cancel(new Set([key]));
  }

  retry(keys: ReadonlySet<string>) {
    this.publish(
      this.items.map((item) =>
        keys.has(item.key) &&
        isRetryAllEligible(item.state, item.retryEligible, true)
          ? {
              ...item,
              state:
                item.targetStatus || item.session
                  ? this.globallyPaused
                    ? 'paused'
                    : 'queued'
                  : 'checking',
              error: undefined,
              retryCount: 0,
              retryAt: undefined,
            }
          : item
      )
    );
  }

  nextRunnable(running: ReadonlySet<string>) {
    if (this.globallyPaused) return [];
    return nextRunnableUploadKeys(
      this.items,
      running,
      MAX_CONCURRENT_UPLOADS - running.size
    );
  }

  markUploading(key: string) {
    this.update(key, (item) => ({
      ...item,
      state: 'uploading',
      error: undefined,
      retryAt: undefined,
    }));
  }

  setSession(key: string, session: UploadSession) {
    this.update(key, (item) => ({
      ...item,
      session,
      uploadedBytes: session.uploadedSize ?? item.uploadedBytes,
      progress: uploadProgress(
        session.uploadedSize ?? item.uploadedBytes,
        item.fingerprint.size
      ),
    }));
  }

  setCommitted(key: string, session: UploadSession, uploadedBytes: number) {
    this.update(key, (item) => ({
      ...item,
      session: { ...session, uploadedSize: uploadedBytes },
      uploadedBytes,
      progress: uploadProgress(uploadedBytes, item.fingerprint.size),
    }));
  }

  setProgress(key: string, uploadedBytes: number) {
    this.setProgressBatch(new Map([[key, uploadedBytes]]));
  }

  setProgressBatch(progressByKey: ReadonlyMap<string, number>) {
    let items: UploadQueueItem[] | undefined;
    for (const [key, uploadedBytes] of progressByKey) {
      const index = this.itemIndex.get(key);
      if (index === undefined) continue;
      const current = (items ?? this.items)[index];
      const progress = Math.max(
        current.progress,
        Math.min(100, uploadProgress(uploadedBytes, current.fingerprint.size))
      );
      if (progress === current.progress) continue;
      if (!items) {
        items = this.items.slice();
        this.onItemsCopied();
      }
      items[index] = { ...current, progress };
    }
    if (!items) return;
    this.items = items;
    this.snapshot = this.createSnapshot();
    for (const listener of this.listeners) listener();
  }

  markRetrying(
    key: string,
    retryCount: number,
    retryAt: number,
    error: string
  ) {
    this.update(key, (item) => ({
      ...item,
      state: 'retrying',
      retryCount,
      retryEligible: true,
      retryAt,
      error,
    }));
  }

  markFailed(
    key: string,
    error: string,
    retryEligible: boolean,
    retryCount = 0
  ) {
    this.update(key, (item) => ({
      ...item,
      state: 'failed',
      error,
      retryEligible,
      retryCount,
      retryAt: undefined,
      progress: uploadProgress(item.uploadedBytes, item.fingerprint.size),
    }));
  }

  markTargetChanged(key: string) {
    this.update(key, (item) => ({
      ...item,
      state: 'checking',
      overwrite: false,
      targetVersion: undefined,
      targetStatus: undefined,
      existingTarget: undefined,
      retryCount: 0,
      retryEligible: false,
      retryAt: undefined,
      error: undefined,
    }));
  }

  markComplete(key: string, session: UploadSession) {
    this.update(key, (item) => ({
      ...item,
      session,
      state: 'complete',
      uploadedBytes: item.fingerprint.size,
      progress: 100,
      retryCount: 0,
      retryEligible: false,
      retryAt: undefined,
      error: undefined,
    }));
  }
}
