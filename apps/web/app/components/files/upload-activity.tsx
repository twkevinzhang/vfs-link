import {
  AlertCircle,
  Check,
  ChevronDown,
  ChevronUp,
  LoaderCircle,
  Pause,
  Play,
  RotateCcw,
  Upload,
  X,
} from 'lucide-react';
import { useCallback, useRef, useState } from 'react';

import {
  useBackgroundUploadQueue,
  type UploadQueueItem,
} from '../../hooks/use-upload-queue';
import { formatBytes } from '../../lib/format';
import { Button } from '../ui/button';

export function UploadActivity({
  queue,
  expanded,
  onExpandedChange,
  onRequestCancelAll,
}: {
  queue: Pick<
    ReturnType<typeof useBackgroundUploadQueue>,
    | 'items'
    | 'summary'
    | 'retry'
    | 'retryAll'
    | 'dismiss'
    | 'cancel'
    | 'pause'
    | 'resume'
    | 'pauseAll'
    | 'resumeAll'
    | 'reconnect'
    | 'authorizeSource'
    | 'replaceOne'
    | 'skipOne'
    | 'replaceAll'
    | 'skipAll'
    | 'globallyPaused'
    | 'isUploadLeader'
  >;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  onRequestCancelAll: () => void;
}) {
  const { items, summary } = queue;
  const roundedProgress = Math.round(summary.progress);
  const pendingCount =
    summary.checking + summary.queued + summary.uploading + summary.retrying;
  const retryableCount = items.filter(
    (item) => item.state === 'failed' && item.retryEligible
  ).length;
  const decisionBatches = Array.from(
    items
      .filter((item) => item.state === 'needs-decision')
      .reduce((batches, item) => {
        const batch = batches.get(item.batchId) ?? [];
        batch.push(item);
        batches.set(item.batchId, batch);
        return batches;
      }, new Map<string, UploadQueueItem[]>())
  );
  const queueListId = 'background-upload-queue';
  const headline =
    summary.needsDecision > 0
      ? `${summary.needsDecision} ${
          summary.needsDecision === 1 ? 'upload needs' : 'uploads need'
        } a decision`
      : summary.uploading > 0
      ? `Uploading ${summary.uploading} ${
          summary.uploading === 1 ? 'file' : 'files'
        }`
      : summary.checking > 0
      ? 'Checking upload paths'
      : summary.queued > 0
      ? 'Preparing uploads'
      : summary.paused > 0
      ? 'Uploads paused'
      : summary.failed > 0
      ? 'Uploads need attention'
      : summary.localMissing > 0
      ? 'Local source needs attention'
      : 'Uploads complete';

  return (
    <section className="grid gap-3 p-3" aria-labelledby="upload-activity-title">
      <fieldset
        disabled={!queue.isUploadLeader}
        className="contents disabled:opacity-70"
      >
        <div className="grid gap-1.5">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <div className="flex min-w-0 items-center gap-2">
              {summary.needsDecision > 0 ? (
                <AlertCircle
                  aria-hidden="true"
                  className="h-4 w-4 shrink-0 text-amber-600"
                />
              ) : pendingCount > 0 ? (
                <LoaderCircle
                  aria-hidden="true"
                  className="h-4 w-4 shrink-0 animate-spin text-accent"
                />
              ) : summary.failed > 0 || summary.localMissing > 0 ? (
                <AlertCircle
                  aria-hidden="true"
                  className="h-4 w-4 shrink-0 text-destructive"
                />
              ) : (
                <Check
                  aria-hidden="true"
                  className="h-4 w-4 shrink-0 text-[#11615a]"
                />
              )}
              <p
                id="upload-activity-title"
                className="truncate text-sm font-semibold"
              >
                {headline}
              </p>
            </div>
            <div className="ml-auto flex flex-wrap items-center justify-end gap-1">
              <p className="text-right text-xs text-muted-foreground">
                <span className="sm:hidden">
                  {formatBytes(summary.uploadedBytes)} · {roundedProgress}%
                </span>
                <span className="hidden sm:inline">
                  {formatBytes(summary.uploadedBytes)} of{' '}
                  {formatBytes(summary.totalBytes)} · {roundedProgress}%
                </span>
              </p>
              {pendingCount + summary.paused + summary.needsDecision > 0 && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-8 px-2 text-destructive hover:text-destructive"
                  onClick={onRequestCancelAll}
                >
                  Cancel all
                </Button>
              )}
              {(pendingCount > 0 || summary.paused > 0) && (
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 px-2"
                  onClick={
                    queue.globallyPaused ? queue.resumeAll : queue.pauseAll
                  }
                >
                  {queue.globallyPaused ? (
                    <Play className="h-3.5 w-3.5" aria-hidden="true" />
                  ) : (
                    <Pause className="h-3.5 w-3.5" aria-hidden="true" />
                  )}
                  {queue.globallyPaused ? 'Resume all' : 'Pause all'}
                </Button>
              )}
              <Button
                variant="outline"
                size="sm"
                className="h-8 px-2"
                disabled={retryableCount === 0}
                onClick={queue.retryAll}
              >
                <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
                Retry all
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                onClick={() => onExpandedChange(!expanded)}
                aria-expanded={expanded}
                aria-controls={queueListId}
                aria-label={
                  expanded ? 'Collapse upload details' : 'Expand upload details'
                }
              >
                {expanded ? (
                  <ChevronDown aria-hidden="true" className="h-4 w-4" />
                ) : (
                  <ChevronUp aria-hidden="true" className="h-4 w-4" />
                )}
              </Button>
            </div>
          </div>
          <UploadProgress value={roundedProgress} label="Overall upload" />
          {expanded && (
            <p className="text-xs text-muted-foreground" aria-live="polite">
              {summary.complete} complete · {summary.queued} queued
              {summary.checking > 0 ? ` · ${summary.checking} checking` : ''}
              {summary.needsDecision > 0
                ? ` · ${summary.needsDecision} need a decision`
                : ''}
              {summary.skipped > 0 ? ` · ${summary.skipped} skipped` : ''}
              {summary.paused > 0 ? ` · ${summary.paused} paused` : ''}
              {summary.retrying > 0 ? ` · ${summary.retrying} retrying` : ''}
              {summary.failed > 0 ? ` · ${summary.failed} failed` : ''}
              {summary.localMissing > 0
                ? ` · ${summary.localMissing} local source missing`
                : ''}
            </p>
          )}
        </div>

        {expanded && (
          <div className="grid gap-3">
            {decisionBatches.map(([batchId, conflicts]) => {
              const duplicateCount = conflicts.filter(
                (item) => item.localDuplicate
              ).length;
              const replaceableCount = conflicts.filter(
                (item) =>
                  !item.localDuplicate && item.targetStatus !== 'directory'
              ).length;
              return (
                <section
                  key={batchId}
                  className="flex flex-col gap-2 rounded-lg border border-amber-300 bg-amber-50 p-3 sm:flex-row sm:items-center"
                  aria-label={`Resolve ${conflicts.length} upload path conflicts`}
                >
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-semibold text-amber-950">
                      {conflicts.length} path{' '}
                      {conflicts.length === 1 ? 'conflict' : 'conflicts'}
                    </p>
                    <p className="text-xs text-amber-900/80">
                      Other files keep uploading while these wait.
                      {duplicateCount > 0
                        ? ' Duplicate local paths must be chosen individually.'
                        : ''}
                    </p>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    <Button
                      size="sm"
                      disabled={replaceableCount === 0}
                      onClick={() => queue.replaceAll(batchId)}
                    >
                      Replace all
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => queue.skipAll(batchId)}
                    >
                      Skip all
                    </Button>
                  </div>
                </section>
              );
            })}
            <ul
              id={queueListId}
              className="grid gap-2"
              aria-label="Upload queue"
            >
              {items.map((item) => (
                <UploadActivityItem
                  key={item.key}
                  item={item}
                  onCancel={() => queue.cancel(item.key)}
                  onRetry={() => queue.retry(item.key)}
                  onPause={() => queue.pause(item.key)}
                  onResume={() => queue.resume(item.key)}
                  onReconnect={(file, handle) =>
                    queue.reconnect(item.key, file, handle)
                  }
                  onAuthorize={() => void queue.authorizeSource(item.key)}
                  onReplace={() => queue.replaceOne(item.key)}
                  onSkip={() => queue.skipOne(item.key)}
                  onDismiss={() => queue.dismiss(item.key)}
                />
              ))}
            </ul>
          </div>
        )}
      </fieldset>
    </section>
  );
}

export function UploadActivityItem({
  item,
  onCancel,
  onRetry,
  onPause,
  onResume,
  onReconnect,
  onAuthorize,
  onReplace,
  onSkip,
  onDismiss,
}: {
  item: UploadQueueItem;
  onCancel: () => void;
  onRetry: () => void;
  onPause: () => void;
  onResume: () => void;
  onReconnect: (
    file: File,
    handle?: FileSystemFileHandle
  ) => string | undefined;
  onAuthorize: () => void;
  onReplace: () => void;
  onSkip: () => void;
  onDismiss: () => void;
}) {
  const roundedProgress = Math.round(item.progress);
  const statusLabel =
    item.state === 'checking'
      ? 'Checking path'
      : item.state === 'needs-decision'
      ? 'Needs decision'
      : item.state === 'skipped'
      ? 'Skipped'
      : item.state === 'queued'
      ? 'Queued'
      : item.state === 'uploading'
      ? `${roundedProgress}%`
      : item.state === 'retrying'
      ? `Retrying · attempt ${item.retryCount + 1}/6`
      : item.state === 'paused'
      ? 'Paused'
      : item.state === 'complete'
      ? 'Complete'
      : item.state === 'local-missing'
      ? '已從本機移除'
      : 'Failed';

  return (
    <li className="grid gap-1.5 rounded-lg border border-border bg-muted/20 p-2.5">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        {item.state === 'complete' || item.state === 'skipped' ? (
          <Check
            aria-hidden="true"
            className="h-4 w-4 shrink-0 text-[#11615a]"
          />
        ) : item.state === 'needs-decision' ? (
          <AlertCircle
            aria-hidden="true"
            className="h-4 w-4 shrink-0 text-amber-600"
          />
        ) : item.state === 'failed' || item.state === 'local-missing' ? (
          <AlertCircle
            aria-hidden="true"
            className="h-4 w-4 shrink-0 text-destructive"
          />
        ) : item.state === 'checking' ||
          item.state === 'uploading' ||
          item.state === 'retrying' ? (
          <LoaderCircle
            aria-hidden="true"
            className="h-4 w-4 shrink-0 animate-spin text-accent"
          />
        ) : (
          <Upload aria-hidden="true" className="h-4 w-4 shrink-0 text-accent" />
        )}
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium" title={item.relativePath}>
            {item.relativePath}
          </p>
          <p
            className="truncate text-xs text-muted-foreground"
            title={`Destination: ${item.destinationPath}`}
          >
            {formatBytes(item.fingerprint.size)} · to {item.destinationPath}
          </p>
        </div>
        <span className="shrink-0 text-xs text-muted-foreground">
          {statusLabel}
        </span>
        {item.state === 'needs-decision' && (
          <div className="flex shrink-0 flex-wrap items-center gap-1">
            <Button
              size="sm"
              onClick={onReplace}
              disabled={item.targetStatus === 'directory'}
            >
              {item.localDuplicate ? 'Use this file' : 'Replace this'}
            </Button>
            <Button size="sm" variant="outline" onClick={onSkip}>
              Skip
            </Button>
          </div>
        )}
        {(item.state === 'queued' ||
          item.state === 'uploading' ||
          item.state === 'retrying') && (
          <Button
            variant="outline"
            size="sm"
            className="h-8 shrink-0 px-2"
            onClick={onPause}
            aria-label={`Pause upload ${item.relativePath}`}
          >
            <Pause className="h-3.5 w-3.5" aria-hidden="true" />
            Pause
          </Button>
        )}
        {(item.state === 'queued' ||
          item.state === 'uploading' ||
          item.state === 'retrying' ||
          item.state === 'paused') && (
          <Button
            variant="ghost"
            size="sm"
            className="h-8 shrink-0 px-2 text-destructive hover:text-destructive"
            onClick={onCancel}
            aria-label={`Cancel upload ${item.relativePath}`}
          >
            Cancel
          </Button>
        )}
        {item.state === 'paused' && item.file && (
          <Button variant="outline" size="sm" onClick={onResume}>
            <Play className="h-3.5 w-3.5" aria-hidden="true" />
            Resume
          </Button>
        )}
        {item.state === 'paused' && !item.file && item.fileHandle && (
          <Button variant="outline" size="sm" onClick={onAuthorize}>
            <Play className="h-3.5 w-3.5" aria-hidden="true" />
            Allow access &amp; resume
          </Button>
        )}
        {item.state === 'failed' && (
          <div className="flex shrink-0 items-center gap-1">
            <Button
              variant="outline"
              size="sm"
              onClick={onRetry}
              disabled={!item.retryEligible}
            >
              <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
              Retry
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8"
              onClick={onDismiss}
              aria-label={`Dismiss ${item.relativePath}`}
            >
              <X className="h-4 w-4" aria-hidden="true" />
            </Button>
          </div>
        )}
        {(item.state === 'local-missing' ||
          (item.state === 'paused' && !item.file && !item.fileHandle)) && (
          <ReconnectSourceButton
            relativePath={item.relativePath}
            onReconnect={onReconnect}
          />
        )}
        {(item.state === 'complete' ||
          item.state === 'skipped' ||
          item.state === 'local-missing') && (
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            onClick={onDismiss}
            aria-label={`Dismiss ${item.relativePath}`}
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </Button>
        )}
      </div>
      {item.state === 'needs-decision' && (
        <p className="text-xs text-amber-800">
          {item.targetStatus === 'directory'
            ? 'A folder already exists at this path. Skip this file or choose another destination.'
            : item.localDuplicate
            ? 'More than one selected file targets this same path. Choose the source to keep.'
            : `A file already exists at this path${
                item.existingTarget
                  ? ` · ${formatBytes(item.existingTarget.size)}`
                  : ''
              }.`}
        </p>
      )}
      {!['needs-decision', 'skipped', 'checking'].includes(item.state) && (
        <UploadProgress value={roundedProgress} label={item.relativePath} />
      )}
      {item.error && (
        <p className="text-xs text-destructive" role="alert">
          {item.error}
        </p>
      )}
    </li>
  );
}

export function UploadProgress({
  value,
  label,
}: {
  value: number;
  label: string;
}) {
  return (
    <div
      className="h-1.5 overflow-hidden rounded-full bg-muted"
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value}
    >
      <div
        className="h-full bg-accent transition-[width]"
        style={{ width: `${value}%` }}
      />
    </div>
  );
}

export function ReconnectSourceButton({
  relativePath,
  onReconnect,
}: {
  relativePath: string;
  onReconnect: (
    file: File,
    handle?: FileSystemFileHandle
  ) => string | undefined;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [error, setError] = useState<string>();

  const acceptFile = useCallback(
    (file: File, handle?: FileSystemFileHandle) => {
      setError(onReconnect(file, handle));
    },
    [onReconnect]
  );

  return (
    <div className="flex shrink-0 items-center gap-1">
      <input
        ref={inputRef}
        type="file"
        className="sr-only"
        aria-label={`重新選擇 ${relativePath}`}
        onChange={(event) => {
          const file = event.target.files?.[0];
          if (file) acceptFile(file);
          event.target.value = '';
        }}
      />
      <Button
        variant="outline"
        size="sm"
        onClick={() => {
          const picker = (
            window as Window & {
              showOpenFilePicker?: (options?: {
                multiple?: boolean;
              }) => Promise<FileSystemFileHandle[]>;
            }
          ).showOpenFilePicker;
          if (!picker) {
            inputRef.current?.click();
            return;
          }
          void picker({ multiple: false })
            .then(async ([handle]) => {
              if (handle) acceptFile(await handle.getFile(), handle);
            })
            .catch((pickerError: unknown) => {
              if ((pickerError as { name?: string }).name !== 'AbortError') {
                setError(
                  pickerError instanceof Error
                    ? pickerError.message
                    : '無法讀取來源檔案。'
                );
              }
            });
        }}
      >
        選擇來源檔案
      </Button>
      {error && (
        <span
          className="max-w-44 truncate text-xs text-destructive"
          title={error}
        >
          {error}
        </span>
      )}
    </div>
  );
}
