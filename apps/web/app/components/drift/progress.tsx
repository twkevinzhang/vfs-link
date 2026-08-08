import {
  AlertTriangle,
  CheckCircle2,
  LoaderCircle,
  RotateCcw,
} from 'lucide-react';

import {
  driftActionPercent,
  driftStatusLabel,
  isDriftActionTerminal,
} from '../../lib/drift';
import { formatDate } from '../../lib/format';
import { cn } from '../../lib/utils';
import type { DriftAction, DriftScan } from '../../types/drift';
import { Button } from '../ui/button';

export function DriftActionProgress({
  action,
  failedPaths,
  canRetry,
  retrying,
  dismissing,
  onRetry,
  onDismiss,
}: {
  action: DriftAction;
  failedPaths: string[];
  canRetry: boolean;
  retrying: boolean;
  dismissing: boolean;
  onRetry: () => void;
  onDismiss: () => void;
}) {
  const terminal = isDriftActionTerminal(action.status);
  const percent = driftActionPercent(action);
  return (
    <section
      className="rounded-xl border border-primary/25 bg-white p-4 shadow-sm"
      aria-live="polite"
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex items-start gap-3">
          {terminal ? (
            action.failed > 0 ? (
              <AlertTriangle className="mt-0.5 h-5 w-5 text-amber-700" />
            ) : (
              <CheckCircle2 className="mt-0.5 h-5 w-5 text-emerald-700" />
            )
          ) : (
            <LoaderCircle className="mt-0.5 h-5 w-5 animate-spin text-primary" />
          )}
          <div>
            <p className="font-semibold">
              Physical move {driftStatusLabel(action.status).toLowerCase()}
            </p>
            <p className="mt-1 break-all font-mono text-[11px] text-muted-foreground">
              {action.id || action.actionId}
            </p>
            <p className="mt-1 text-sm text-muted-foreground">
              {action.progress.toLocaleString()} of{' '}
              {action.total.toLocaleString()} processed ·{' '}
              {action.succeeded.toLocaleString()} succeeded ·{' '}
              {action.failed.toLocaleString()} failed
            </p>
            {action.error && (
              <p className="mt-1 text-sm text-destructive">{action.error}</p>
            )}
            {terminal && (
              <p className="mt-1 text-xs text-muted-foreground">
                The table still shows the cached snapshot. Use Manual rescan
                when you are ready to refresh the full inventory.
              </p>
            )}
          </div>
        </div>
        <div className="flex gap-2">
          {failedPaths.length > 0 && (
            <Button
              variant="outline"
              size="sm"
              disabled={!canRetry || retrying}
              onClick={onRetry}
            >
              <RotateCcw className="h-4 w-4" />
              Retry {failedPaths.length} failed
            </Button>
          )}
          {terminal && (
            <Button
              variant="ghost"
              size="sm"
              disabled={dismissing}
              onClick={onDismiss}
            >
              {dismissing && <LoaderCircle className="h-4 w-4 animate-spin" />}
              {dismissing ? 'Dismissing' : 'Dismiss'}
            </Button>
          )}
        </div>
      </div>
      <div className="mt-3 h-2 overflow-hidden rounded-full bg-muted">
        <div
          className="h-full rounded-full bg-primary transition-[width] duration-500"
          style={{ width: `${percent}%` }}
        />
      </div>
    </section>
  );
}

export function DriftScanProgress({
  scan,
  retrying,
  onRetry,
}: {
  scan: DriftScan;
  retrying: boolean;
  onRetry: () => void;
}) {
  const running = scan.status === 'pending' || scan.status === 'running';
  const completed = scan.status === 'completed';
  return (
    <section
      className="rounded-xl border border-primary/25 bg-white p-4 shadow-sm"
      aria-live="polite"
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex items-start gap-3">
          {running ? (
            <LoaderCircle className="mt-0.5 h-5 w-5 animate-spin text-primary" />
          ) : completed ? (
            <CheckCircle2 className="mt-0.5 h-5 w-5 text-emerald-700" />
          ) : (
            <AlertTriangle className="mt-0.5 h-5 w-5 text-amber-700" />
          )}
          <div>
            <p className="font-semibold">Storage rescan {scan.status}</p>
            <p className="mt-1 break-all font-mono text-[11px] text-muted-foreground">
              {scan.id}
            </p>
            <p className="mt-1 text-sm text-muted-foreground">
              {scanPhaseLabel(scan.phase)} · started{' '}
              {formatDate(scan.createdAt)}
            </p>
            {scan.error && (
              <p className="mt-1 text-sm text-destructive">{scan.error}</p>
            )}
          </div>
        </div>
        {scan.status === 'failed' && (
          <Button
            variant="outline"
            size="sm"
            disabled={retrying}
            onClick={onRetry}
          >
            {retrying ? (
              <LoaderCircle className="h-4 w-4 animate-spin" />
            ) : (
              <RotateCcw className="h-4 w-4" />
            )}
            Retry rescan
          </Button>
        )}
      </div>
      <div className="mt-3 h-2 overflow-hidden rounded-full bg-muted">
        <div
          className={cn(
            'h-full rounded-full transition-[width] duration-500',
            completed
              ? 'bg-primary'
              : scan.status === 'failed'
              ? 'bg-destructive'
              : 'animate-pulse bg-primary'
          )}
          style={{
            width: completed || scan.status === 'failed' ? '100%' : '65%',
          }}
        />
      </div>
    </section>
  );
}

function scanPhaseLabel(phase: DriftScan['phase']) {
  switch (phase) {
    case 'queued':
      return 'Waiting to start';
    case 'metadata':
      return 'Scanning file metadata';
    case 'objects':
      return 'Listing storage objects';
    case 'saving':
      return 'Saving the new snapshot';
    case 'completed':
      return 'Snapshot refreshed';
    case 'failed':
      return 'Rescan failed';
  }
}
