import {
  ArrowRight,
  CheckCircle2,
  LoaderCircle,
  RefreshCcw,
} from 'lucide-react';

import { driftStatusLabel, formatUsdRange } from './drift-formatters';
import { isActionableDriftItem } from '../domain/drift-policy';
import { cn, formatBytes } from './presentation-utils';
import type { DriftItem } from '../domain/drift';
import { Badge } from '../../../shared/presentation/ui/badge';
import { Button } from '../../../shared/presentation/ui/button';
import { Checkbox } from '../../../shared/presentation/ui/checkbox';

type SelectionProps = {
  selected: Set<string>;
  canAct: boolean;
  onToggle: (path: string, checked: boolean) => void;
};

export function DriftTable({
  items,
  selected,
  canAct,
  allActionableSelected,
  onToggleAll,
  onToggle,
}: SelectionProps & {
  items: DriftItem[];
  allActionableSelected: boolean;
  onToggleAll: (checked: boolean) => void;
}) {
  return (
    <table className="w-full min-w-[1120px] table-fixed text-left text-sm">
      <thead className="border-b border-border bg-white text-[11px] uppercase tracking-[0.1em] text-muted-foreground">
        <tr>
          <th className="w-11 px-4 py-3">
            <Checkbox
              aria-label="Select all actionable drift items on this page"
              checked={allActionableSelected}
              disabled={!canAct || !items.some(isActionableDriftItem)}
              onChange={(event) => onToggleAll(event.target.checked)}
            />
          </th>
          <th className="w-[22%] px-2 py-3">Logical path</th>
          <th className="w-[23%] px-2 py-3">Object key move</th>
          <th className="w-[12%] px-2 py-3">Status</th>
          <th className="w-[11%] px-2 py-3">Size / class</th>
          <th className="w-[15%] px-2 py-3">Generation</th>
          <th className="w-[13%] px-4 py-3 text-right">Estimated cost</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-border">
        {items.map((item) => {
          const actionable = canAct && isActionableDriftItem(item);
          return (
            <tr
              key={item.logicPath}
              className={cn(
                'align-top hover:bg-[#f8faf7]',
                selected.has(item.logicPath) && 'bg-[#edf6f3]'
              )}
            >
              <td className="px-4 py-3.5">
                <Checkbox
                  aria-label={`Select ${item.logicPath}`}
                  checked={selected.has(item.logicPath)}
                  disabled={!actionable}
                  onChange={(event) =>
                    onToggle(item.logicPath, event.target.checked)
                  }
                />
              </td>
              <td className="px-2 py-3.5">
                <p className="break-words font-medium leading-5">
                  {item.logicPath}
                </p>
                {item.error && (
                  <p className="mt-1 text-xs text-destructive">{item.error}</p>
                )}
              </td>
              <td className="px-2 py-3.5 text-xs">
                <KeyMove
                  currentKey={item.currentKey}
                  targetKey={item.targetKey}
                  status={item.status}
                />
              </td>
              <td className="px-2 py-3.5">
                <DriftStatus status={item.status} />
              </td>
              <td className="px-2 py-3.5">
                <p className="font-medium tabular-nums">
                  {formatBytes(item.size)}
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {item.storageClass || '—'}
                </p>
              </td>
              <td className="px-2 py-3.5 font-mono text-xs text-muted-foreground">
                <span className="break-all">{item.generation || '—'}</span>
              </td>
              <td className="px-4 py-3.5 text-right font-medium tabular-nums">
                {formatUsdRange(
                  item.estimatedCostUsdMin,
                  item.estimatedCostUsdMax
                )}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

export function DriftMobileCard({
  item,
  checked,
  canAct,
  onToggle,
}: Omit<SelectionProps, 'selected'> & {
  item: DriftItem;
  checked: boolean;
}) {
  return (
    <article className={cn('grid gap-3 p-4', checked && 'bg-[#edf6f3]')}>
      <div className="flex items-start gap-3">
        <Checkbox
          aria-label={`Select ${item.logicPath}`}
          checked={checked}
          disabled={!canAct || !isActionableDriftItem(item)}
          onChange={(event) => onToggle(item.logicPath, event.target.checked)}
          className="mt-1"
        />
        <div className="min-w-0 flex-1">
          <p className="break-words font-medium leading-5">{item.logicPath}</p>
          <div className="mt-2 flex items-center justify-between gap-3">
            <DriftStatus status={item.status} />
            <span className="font-semibold tabular-nums">
              {formatUsdRange(
                item.estimatedCostUsdMin,
                item.estimatedCostUsdMax
              )}
            </span>
          </div>
        </div>
      </div>
      <div className="rounded-lg border border-border bg-[#fbfaf6] p-3 text-xs">
        <KeyMove
          currentKey={item.currentKey}
          targetKey={item.targetKey}
          status={item.status}
        />
      </div>
      <dl className="grid grid-cols-3 gap-2 text-xs">
        <div>
          <dt className="text-muted-foreground">Size</dt>
          <dd className="mt-1 font-medium">{formatBytes(item.size)}</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">Class</dt>
          <dd className="mt-1 font-medium">{item.storageClass || '—'}</dd>
        </div>
        <div className="min-w-0">
          <dt className="text-muted-foreground">Generation</dt>
          <dd className="mt-1 truncate font-mono">{item.generation || '—'}</dd>
        </div>
      </dl>
      {item.error && <p className="text-xs text-destructive">{item.error}</p>}
    </article>
  );
}

function KeyMove({
  currentKey,
  targetKey,
  status,
}: {
  currentKey: string;
  targetKey: string;
  status: string;
}) {
  const aligned = status.toLowerCase() === 'aligned';
  if (aligned) {
    return (
      <div className="grid min-w-0 gap-1">
        <span className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
          Matches index
        </span>
        <span className="break-all font-medium text-foreground">
          {targetKey || currentKey || '—'}
        </span>
      </div>
    );
  }
  return (
    <div className="grid min-w-0 gap-1 text-muted-foreground">
      <span className="text-[10px] font-medium uppercase tracking-wide">
        Current GCS key
      </span>
      <span className="break-all line-through decoration-border">
        {currentKey || '—'}
      </span>
      <span className="flex items-start gap-1.5 font-medium text-foreground">
        <ArrowRight className="mt-0.5 h-3 w-3 shrink-0 text-primary" />
        <span className="min-w-0">
          <span className="block text-[10px] uppercase tracking-wide text-muted-foreground">
            Expected key
          </span>
          <span className="break-all">{targetKey || '—'}</span>
        </span>
      </span>
    </div>
  );
}

function DriftStatus({ status }: { status: string }) {
  const normalized = status.toLowerCase();
  return (
    <Badge
      variant="outline"
      className={cn(
        'whitespace-nowrap',
        [
          'drifted',
          'misaligned',
          'ready',
          'key_mismatch',
          'object_key_mismatch',
          'size_mismatch',
          'target_conflict',
          'shared_object',
        ].includes(normalized) && 'border-amber-300 bg-amber-50 text-amber-900',
        normalized === 'aligned' &&
          'border-emerald-300 bg-emerald-50 text-emerald-800',
        ['missing', 'object_missing', 'failed', 'error'].includes(normalized) &&
          'border-red-300 bg-red-50 text-red-800',
        normalized === 'orphan_object' &&
          'border-slate-300 bg-slate-50 text-slate-800',
        ['moving', 'running'].includes(normalized) &&
          'border-blue-300 bg-blue-50 text-blue-800'
      )}
    >
      {driftStatusLabel(status)}
    </Badge>
  );
}

export function DriftLoadingRows() {
  return (
    <div className="grid gap-3 p-4">
      {[0, 1, 2, 3].map((row) => (
        <div key={row} className="h-14 animate-pulse rounded-lg bg-muted/70" />
      ))}
    </div>
  );
}

export function EmptyDriftState({
  hasFilter,
  hasSnapshot,
  scanning,
  onRescan,
}: {
  hasFilter: boolean;
  hasSnapshot: boolean;
  scanning: boolean;
  onRescan: () => void;
}) {
  const needsSnapshot = !hasSnapshot && !hasFilter;
  return (
    <div className="grid min-h-64 place-items-center p-8 text-center">
      <div>
        <span className="mx-auto grid h-12 w-12 place-items-center rounded-full bg-secondary">
          <CheckCircle2 className="h-6 w-6 text-primary" />
        </span>
        <h2 className="mt-4 font-semibold">
          {hasFilter
            ? 'No matching drift records'
            : needsSnapshot
            ? 'No drift snapshot yet'
            : 'Storage keys are aligned'}
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          {hasFilter
            ? 'Adjust the search or status filter.'
            : needsSnapshot
            ? 'Run a manual full scan to build the first cached snapshot.'
            : 'No physical object move is needed.'}
        </p>
        {needsSnapshot && (
          <Button className="mt-4" disabled={scanning} onClick={onRescan}>
            {scanning ? (
              <LoaderCircle className="h-4 w-4 animate-spin" />
            ) : (
              <RefreshCcw className="h-4 w-4" />
            )}
            Run first full scan
          </Button>
        )}
      </div>
    </div>
  );
}
