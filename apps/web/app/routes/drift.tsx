import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  CircleDollarSign,
  CloudCog,
  DatabaseZap,
  ExternalLink,
  LoaderCircle,
  RefreshCcw,
  RotateCcw,
  Search,
  ShieldAlert,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, type MetaFunction } from 'react-router';

import { Alert } from '../components/ui/alert';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '../components/ui/alert-dialog';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Card } from '../components/ui/card';
import { Checkbox } from '../components/ui/checkbox';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from '../components/ui/dialog';
import { Input } from '../components/ui/input';
import {
  createDriftAction,
  createDriftPlan,
  getDrift,
  getDriftAction,
} from '../lib/api';
import {
  driftActionFailedPaths,
  driftActionPercent,
  driftMethodLabel,
  driftStatusLabel,
  formatUsd,
  formatUsdRange,
  isActionableDriftItem,
  isDriftActionTerminal,
} from '../lib/drift';
import { FILES_ROUTE } from '../lib/file-route';
import { formatBytes, formatDate } from '../lib/format';
import { cn } from '../lib/utils';
import type {
  DriftAction,
  DriftCostItem,
  DriftItem,
  DriftPlan,
  DriftResponse,
} from '../types/drift';

export const meta: MetaFunction = () => [
  { title: 'Storage drift · vfs-link' },
  {
    name: 'description',
    content: 'Review and explicitly reconcile logical paths with GCS keys.',
  },
];

const PAGE_SIZE = 50;
const SEARCH_DEBOUNCE_MS = 250;
const ACTION_POLL_MS = 1500;

type LoadState = {
  data?: DriftResponse;
  loading: boolean;
  error?: string;
};

export default function DriftRoute() {
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [debouncedQuery, setDebouncedQuery] = useState('');
  const [status, setStatus] = useState('all');
  const [offset, setOffset] = useState(0);
  const [state, setState] = useState<LoadState>({ loading: true });
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [plan, setPlan] = useState<DriftPlan>();
  const [planning, setPlanning] = useState(false);
  const [planError, setPlanError] = useState<string>();
  const [costAcknowledged, setCostAcknowledged] = useState(false);
  const [activeAction, setActiveAction] = useState<DriftAction>();
  const [activeActionPaths, setActiveActionPaths] = useState<string[]>([]);
  const [startingAction, setStartingAction] = useState(false);
  const requestRef = useRef(0);
  const disposedRef = useRef(false);

  const loadDrift = useCallback(
    async (nextOffset = offset, refresh = false) => {
      const requestId = requestRef.current + 1;
      requestRef.current = requestId;
      setState((previous) => ({
        ...previous,
        loading: true,
        error: undefined,
      }));
      try {
        const data = await getDrift({
          query: debouncedQuery,
          status,
          limit: PAGE_SIZE,
          offset: nextOffset,
          refresh,
        });
        if (requestRef.current !== requestId) return;
        setState({ data, loading: false });
      } catch (error) {
        if (requestRef.current !== requestId) return;
        const message =
          error instanceof Error
            ? error.message
            : 'Unable to scan storage drift';
        const normalizedMessage = message.toLowerCase();
        if (normalizedMessage.includes('no drift snapshot')) {
          setState({
            data: emptyDriftResponse(debouncedQuery, nextOffset),
            loading: false,
          });
          return;
        }
        if (
          normalizedMessage.includes('drift feature is disabled') ||
          normalizedMessage.includes('does not support drift') ||
          normalizedMessage.includes('storage_driver=gcs')
        ) {
          setState({
            data: {
              ...emptyDriftResponse(debouncedQuery, nextOffset),
              available: false,
              reason: message,
            },
            loading: false,
          });
          return;
        }
        setState((previous) => ({
          ...previous,
          loading: false,
          error: message,
        }));
      }
    },
    [debouncedQuery, offset, status]
  );

  useEffect(() => {
    const timeout = window.setTimeout(() => {
      setOffset(0);
      setDebouncedQuery(query.trim());
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timeout);
  }, [query]);

  useEffect(() => {
    void loadDrift();
  }, [loadDrift]);

  useEffect(() => {
    setSelected(new Set());
  }, [debouncedQuery, offset, status]);

  useEffect(
    () => () => {
      disposedRef.current = true;
    },
    []
  );

  const watchAction = useCallback(async (id: string, paths: string[]) => {
    try {
      for (;;) {
        const next = await getDriftAction(id, paths);
        if (disposedRef.current) return;
        setActiveAction(next);
        if (isDriftActionTerminal(next.status)) {
          return;
        }
        await new Promise((resolve) =>
          window.setTimeout(resolve, ACTION_POLL_MS)
        );
        if (disposedRef.current) return;
      }
    } catch (error) {
      if (disposedRef.current) return;
      setPlanError(
        error instanceof Error
          ? error.message
          : 'Unable to monitor drift action'
      );
    }
  }, []);

  const data = state.data;
  const items = data?.items ?? [];
  const pagination = data?.pagination;
  const storageIsGcs =
    !data?.storageDriver || data.storageDriver.toLowerCase() === 'gcs';
  const canAct = Boolean(
    data &&
      data.available !== false &&
      data.enabled !== false &&
      data.readOnly !== true &&
      storageIsGcs
  );
  const actionableItems = useMemo(
    () => items.filter(isActionableDriftItem),
    [items]
  );
  const allActionableSelected =
    actionableItems.length > 0 &&
    actionableItems.every((item) => selected.has(item.logicPath));
  const failedPaths = activeAction ? driftActionFailedPaths(activeAction) : [];

  const toggleItem = (path: string, checked: boolean) => {
    setSelected((previous) => {
      const next = new Set(previous);
      if (checked) next.add(path);
      else next.delete(path);
      return next;
    });
  };

  const toggleAll = (checked: boolean) => {
    setSelected((previous) => {
      const next = new Set(previous);
      for (const item of actionableItems) {
        if (checked) next.add(item.logicPath);
        else next.delete(item.logicPath);
      }
      return next;
    });
  };

  const preparePlan = async (paths: string[]) => {
    if (paths.length === 0 || !canAct) return;
    setPlanning(true);
    setPlanError(undefined);
    try {
      const nextPlan = await createDriftPlan(paths);
      setPlan(nextPlan);
      setCostAcknowledged(false);
    } catch (error) {
      setPlanError(
        error instanceof Error ? error.message : 'Unable to create drift plan'
      );
    } finally {
      setPlanning(false);
    }
  };

  const startAction = async () => {
    if (!plan || !costAcknowledged) return;
    setStartingAction(true);
    setPlanError(undefined);
    try {
      const action = await createDriftAction(plan.planId, plan.paths);
      setPlan(undefined);
      setSelected(new Set());
      setActiveAction(action);
      setActiveActionPaths(plan.paths);
      const id = action.id || action.actionId;
      if (id && !isDriftActionTerminal(action.status)) {
        void watchAction(id, plan.paths);
      }
    } catch (error) {
      setPlanError(
        error instanceof Error ? error.message : 'Unable to start drift action'
      );
    } finally {
      setStartingAction(false);
    }
  };

  const retryAction = async () => {
    if (!activeAction || !activeAction.idempotencyKey) return;
    setPlanning(true);
    setPlanError(undefined);
    try {
      const action = await createDriftAction(
        activeAction.planId,
        activeActionPaths,
        activeAction.idempotencyKey
      );
      setActiveAction(action);
      const id = action.id || action.actionId;
      if (id && !isDriftActionTerminal(action.status)) {
        void watchAction(id, activeActionPaths);
      }
    } catch (error) {
      setPlanError(
        error instanceof Error ? error.message : 'Unable to retry drift action'
      );
    } finally {
      setPlanning(false);
    }
  };

  return (
    <main className="min-h-screen bg-background text-foreground">
      <div className="mx-auto flex w-full max-w-[1500px] flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8 lg:py-7">
        <header className="relative overflow-hidden rounded-xl border border-primary/25 bg-[#123f3b] px-5 py-5 text-primary-foreground shadow-lg shadow-primary/10 sm:px-7 sm:py-6">
          <div className="pointer-events-none absolute -right-16 -top-24 h-64 w-64 rounded-full border-[32px] border-white/5" />
          <div className="relative flex flex-col gap-5 lg:flex-row lg:items-end lg:justify-between">
            <div className="min-w-0">
              <Button
                variant="ghost"
                size="sm"
                className="-ml-3 mb-3 text-primary-foreground hover:bg-white/10 hover:text-white"
                onClick={() => navigate(FILES_ROUTE)}
              >
                <ArrowLeft className="h-4 w-4" />
                Back to files
              </Button>
              <div className="flex items-start gap-3">
                <span className="rounded-lg border border-white/15 bg-white/10 p-2.5">
                  <DatabaseZap className="h-6 w-6" aria-hidden="true" />
                </span>
                <div>
                  <p className="mb-1 text-xs font-semibold uppercase tracking-[0.22em] text-[#9dd7cf]">
                    GCS object alignment
                  </p>
                  <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">
                    Storage drift control
                  </h1>
                  <p className="mt-2 max-w-2xl text-sm leading-6 text-[#d2e8e4]">
                    Review mismatched object keys, estimate move cost, then move
                    only the paths you explicitly approve.
                  </p>
                </div>
              </div>
            </div>
            <div className="flex flex-col gap-2 sm:flex-row">
              <Button
                variant="outline"
                className="border-white/25 bg-white/5 text-white hover:bg-white/10 hover:text-white"
                onClick={() => void loadDrift(0, true)}
                disabled={state.loading || data?.available === false}
              >
                <RefreshCcw
                  className={cn('h-4 w-4', state.loading && 'animate-spin')}
                />
                Manual rescan
              </Button>
              <Button
                className="bg-[#e8c878] text-[#183633] hover:bg-[#f0d792]"
                disabled={!canAct || selected.size === 0 || planning}
                onClick={() => void preparePlan([...selected])}
              >
                {planning ? (
                  <LoaderCircle className="h-4 w-4 animate-spin" />
                ) : (
                  <CircleDollarSign className="h-4 w-4" />
                )}
                Estimate {selected.size > 0 ? `${selected.size} selected` : ''}
              </Button>
            </div>
          </div>
        </header>

        {!canAct && data && (
          <Alert className="border-amber-300 bg-amber-50 text-amber-950">
            <div className="flex items-start gap-3">
              <ShieldAlert className="mt-0.5 h-5 w-5 shrink-0" />
              <div>
                <p className="font-semibold">Drift actions unavailable</p>
                <p className="mt-1 text-sm leading-5">
                  {data.reason ||
                    (storageIsGcs
                      ? 'This feature is disabled or currently read-only.'
                      : `Object moves require STORAGE_DRIVER=gcs; current driver is ${data.storageDriver}.`)}{' '}
                  Scanning remains read-only.
                </p>
              </div>
            </div>
          </Alert>
        )}

        {(state.error || planError) && (
          <Alert className="border-destructive/30 bg-red-50 text-destructive">
            <div className="flex items-start gap-3">
              <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0" />
              <div>
                <p className="font-semibold">Drift operation unavailable</p>
                <p className="mt-1 text-sm text-foreground">
                  {planError || state.error}
                </p>
              </div>
            </div>
          </Alert>
        )}

        {activeAction && (
          <ActionProgress
            action={activeAction}
            failedPaths={failedPaths}
            canRetry={canAct && Boolean(activeAction.idempotencyKey)}
            retrying={planning}
            onRetry={() => void retryAction()}
            onDismiss={() => setActiveAction(undefined)}
          />
        )}

        <SummaryGrid data={data} loading={state.loading && !data} />

        <section className="overflow-hidden rounded-xl border border-border bg-white shadow-sm">
          <div className="grid gap-3 border-b border-border bg-[#fbfaf6] p-4 lg:grid-cols-[minmax(260px,1fr)_190px_auto] lg:items-center">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search logical path or object key"
                className="h-9 bg-white pl-9"
              />
            </div>
            <label className="sr-only" htmlFor="drift-status">
              Filter by status
            </label>
            <select
              id="drift-status"
              value={status}
              onChange={(event) => {
                setStatus(event.target.value);
                setOffset(0);
              }}
              className="h-9 rounded-md border border-input bg-white px-3 text-sm shadow-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/25"
            >
              <option value="all">All statuses</option>
              <option value="drifted">Drifted</option>
              <option value="aligned">Aligned</option>
              <option value="object_missing">Object missing</option>
              <option value="size_mismatch">Size mismatch</option>
              <option value="target_conflict">Target conflict</option>
              <option value="shared_object">Shared object</option>
              <option value="orphan_object">Orphan object</option>
            </select>
            <div className="flex items-center justify-between gap-3 lg:justify-end">
              <span className="text-right text-xs leading-5 text-muted-foreground">
                Snapshot {formatDate(data?.generatedAt ?? '')}
                <br />
                Pricing {formatPricingDate(data?.pricingAsOf ?? '')}
              </span>
              {selected.size > 0 && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setSelected(new Set())}
                >
                  Clear
                </Button>
              )}
            </div>
          </div>

          {state.loading && !data ? (
            <LoadingRows />
          ) : items.length === 0 ? (
            <EmptyDriftState
              hasFilter={Boolean(debouncedQuery || status !== 'all')}
              hasSnapshot={Boolean(data?.generatedAt)}
              scanning={state.loading}
              onRescan={() => void loadDrift(0, true)}
            />
          ) : (
            <>
              <div className="hidden overflow-x-auto md:block">
                <DriftTable
                  items={items}
                  selected={selected}
                  canAct={canAct}
                  allActionableSelected={allActionableSelected}
                  onToggleAll={toggleAll}
                  onToggle={toggleItem}
                />
              </div>
              <div className="grid divide-y divide-border md:hidden">
                {items.map((item) => (
                  <DriftMobileCard
                    key={item.logicPath}
                    item={item}
                    checked={selected.has(item.logicPath)}
                    canAct={canAct}
                    onToggle={toggleItem}
                  />
                ))}
              </div>
            </>
          )}

          {pagination && pagination.total > 0 && (
            <div className="flex flex-col gap-3 border-t border-border bg-[#fbfaf6] px-4 py-3 text-sm sm:flex-row sm:items-center sm:justify-between">
              <p className="text-muted-foreground">
                {pagination.offset + 1}–
                {Math.min(
                  pagination.offset + pagination.limit,
                  pagination.total
                )}{' '}
                of {pagination.total.toLocaleString()}
              </p>
              <div className="flex gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!pagination.hasPrev || state.loading}
                  onClick={() =>
                    setOffset(Math.max(0, offset - pagination.limit))
                  }
                >
                  <ChevronLeft className="h-4 w-4" />
                  Previous
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!pagination.hasNext || state.loading}
                  onClick={() => setOffset(offset + pagination.limit)}
                >
                  Next
                  <ChevronRight className="h-4 w-4" />
                </Button>
              </div>
            </div>
          )}
        </section>
      </div>

      <AlertDialog
        open={Boolean(plan)}
        onOpenChange={(open) => {
          if (!open && !startingAction) setPlan(undefined);
        }}
      >
        <AlertDialogContent className="max-h-[calc(100vh-2rem)] max-w-2xl overflow-y-auto">
          <AlertDialogTitle className="flex items-center gap-2 text-lg font-semibold">
            <CircleDollarSign className="h-5 w-5 text-primary" />
            Confirm physical object moves
          </AlertDialogTitle>
          <AlertDialogDescription className="text-sm leading-6 text-muted-foreground">
            This action executes the method selected by the plan. Depending on
            the storage driver, that may be an atomic move or a copy, verify,
            and delete sequence. Cost is estimated and may vary.
          </AlertDialogDescription>
          {plan && (
            <div className="grid gap-4">
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                <PlanMetric label="Objects" value={String(plan.items.length)} />
                <PlanMetric
                  label="Data moved"
                  value={formatBytes(plan.totalBytes)}
                />
                <PlanMetric
                  label="Estimated cost"
                  value={formatUsdRange(
                    plan.estimatedCostUsdMin,
                    plan.estimatedCostUsdMax
                  )}
                  emphasize
                />
                <PlanMetric
                  label="Pricing as of"
                  value={formatDate(plan.pricingAsOf)}
                />
                <PlanMetric
                  label="Plan method"
                  value={driftMethodLabel(plan.method)}
                />
              </div>
              <div className="max-h-56 overflow-auto rounded-lg border border-border">
                {plan.items.map((item) => (
                  <div
                    key={item.logicPath}
                    className="grid gap-1 border-b border-border px-3 py-2.5 text-xs last:border-0 sm:grid-cols-[minmax(0,1fr)_auto]"
                  >
                    <div className="min-w-0">
                      <p className="truncate font-medium text-foreground">
                        {item.logicPath}
                      </p>
                      <p className="mt-1 flex min-w-0 items-center gap-1 text-muted-foreground">
                        <span className="truncate">{item.currentKey}</span>
                        <ArrowRight className="h-3 w-3 shrink-0" />
                        <span className="truncate">{item.targetKey}</span>
                      </p>
                    </div>
                    <span className="font-medium text-foreground">
                      {driftMethodLabel(item.method)} ·{' '}
                      {item.estimatedCostUsdMin === 0 &&
                      item.estimatedCostUsdMax === 0 &&
                      plan.estimatedCostUsdMax > 0
                        ? 'Included in total'
                        : formatUsdRange(
                            item.estimatedCostUsdMin,
                            item.estimatedCostUsdMax
                          )}
                    </span>
                  </div>
                ))}
              </div>
              {plan.costBreakdown && plan.costBreakdown.length > 0 && (
                <div className="rounded-lg border border-border bg-[#fbfaf6] p-3">
                  <p className="text-xs font-semibold uppercase tracking-[0.1em] text-muted-foreground">
                    Cost breakdown
                  </p>
                  <div className="mt-2 grid gap-2">
                    {plan.costBreakdown.map((cost) => (
                      <div
                        key={cost.name}
                        className="flex items-start justify-between gap-4 text-xs"
                      >
                        <div>
                          <p className="font-medium text-foreground">
                            {cost.name}
                          </p>
                          <p className="mt-0.5 text-muted-foreground">
                            {cost.details}
                          </p>
                        </div>
                        <span className="shrink-0 font-medium tabular-nums">
                          {formatUsdRange(cost.usdMin, cost.usdMax)}
                        </span>
                      </div>
                    ))}
                  </div>
                </div>
              )}
              {plan.warnings?.map((warning) => (
                <p key={warning} className="text-xs leading-5 text-amber-900">
                  <AlertTriangle className="mr-1 inline h-3.5 w-3.5" />
                  {warning}
                </p>
              ))}
              <label className="flex cursor-pointer items-start gap-3 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm leading-5 text-amber-950">
                <Checkbox
                  checked={costAcknowledged}
                  onChange={(event) =>
                    setCostAcknowledged(event.target.checked)
                  }
                  className="mt-0.5"
                />
                I understand this may perform billed object storage operations
                according to the plan method and may take time to complete.
              </label>
            </div>
          )}
          <div className="flex justify-end gap-2">
            <AlertDialogCancel asChild>
              <Button variant="outline" disabled={startingAction}>
                Cancel
              </Button>
            </AlertDialogCancel>
            <AlertDialogAction asChild>
              <Button
                disabled={!costAcknowledged || startingAction}
                onClick={() => void startAction()}
              >
                {startingAction && (
                  <LoaderCircle className="h-4 w-4 animate-spin" />
                )}
                Start physical moves
              </Button>
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </main>
  );
}

function emptyDriftResponse(query: string, offset: number): DriftResponse {
  return {
    summary: {
      total: 0,
      aligned: 0,
      drifted: 0,
      missing: 0,
      failed: 0,
      totalBytes: 0,
      estimatedCostUsdMin: 0,
      estimatedCostUsdMax: 0,
      costBreakdown: [],
      costFormula: { minimum: '', maximum: '' },
      warnings: [],
    },
    items: [],
    pagination: {
      limit: PAGE_SIZE,
      offset,
      total: 0,
      query,
      hasNext: false,
      hasPrev: false,
    },
    pricingAsOf: '',
    pricingModel: '',
    pricingSources: [],
    generatedAt: '',
    enabled: true,
  };
}

function SummaryGrid({
  data,
  loading,
}: {
  data?: DriftResponse;
  loading: boolean;
}) {
  const summary = data?.summary;
  const metrics = [
    {
      label: 'Drifted objects',
      value: summary?.drifted.toLocaleString() ?? '—',
      detail: `${summary?.total.toLocaleString() ?? '—'} scanned`,
      icon: DatabaseZap,
    },
    {
      label: 'Data affected',
      value:
        summary && Number.isFinite(summary.totalBytes)
          ? formatBytes(summary.totalBytes)
          : '—',
      detail: `${summary?.missing.toLocaleString() ?? '—'} missing`,
      icon: CloudCog,
    },
    {
      label: 'Estimated move cost',
      value: summary
        ? formatUsdRange(
            summary.estimatedCostUsdMin,
            summary.estimatedCostUsdMax
          )
        : '—',
      detail: 'No move until confirmed',
      icon: CircleDollarSign,
    },
    {
      label: 'Aligned',
      value: summary?.aligned.toLocaleString() ?? '—',
      detail: `${summary?.failed.toLocaleString() ?? '—'} failed checks`,
      icon: CheckCircle2,
    },
  ];

  return (
    <Dialog>
      <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {metrics.map((metric) => {
          const content = (
            <>
              <span className="flex items-start justify-between gap-3">
                <span className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                  {metric.label}
                </span>
                <metric.icon className="h-4 w-4 text-primary" />
              </span>
              <span
                className={cn(
                  'mt-4 block text-2xl font-semibold tabular-nums',
                  loading && 'animate-pulse text-muted'
                )}
              >
                {metric.value}
              </span>
              <span className="mt-1 block text-xs text-muted-foreground">
                {metric.detail}
              </span>
            </>
          );
          if (metric.label === 'Estimated move cost') {
            return (
              <Card
                key={metric.label}
                className="overflow-hidden shadow-none transition-colors hover:border-primary/60 hover:bg-[#fbfdfc]"
              >
                <DialogTrigger asChild>
                  <button
                    type="button"
                    aria-label="View estimated move cost details"
                    className="group block w-full p-4 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
                  >
                    {content}
                    <span className="mt-3 inline-flex items-center gap-1 text-xs font-medium text-primary opacity-80 transition-opacity group-hover:opacity-100">
                      View calculation
                      <ArrowRight className="h-3 w-3" />
                    </span>
                  </button>
                </DialogTrigger>
              </Card>
            );
          }
          return (
            <Card
              key={metric.label}
              className="overflow-hidden p-4 shadow-none"
            >
              {content}
            </Card>
          );
        })}
      </section>
      <CostEstimateDialog data={data} />
    </Dialog>
  );
}

function CostEstimateDialog({ data }: { data?: DriftResponse }) {
  const summary = data?.summary;
  const breakdown = summary?.costBreakdown ?? [];
  const hasEstimate = Boolean(
    summary && data?.pricingAsOf && summary.costFormula.minimum
  );

  return (
    <DialogContent className="max-h-[calc(100dvh-1rem)] w-[calc(100%-1rem)] max-w-3xl gap-5 rounded-xl p-4 sm:max-h-[calc(100dvh-2rem)] sm:w-[calc(100%-2rem)] sm:p-6">
      <div className="pr-8">
        <DialogTitle className="flex items-center gap-2 text-lg font-semibold">
          <CircleDollarSign className="h-5 w-5 text-primary" />
          Estimated move cost
        </DialogTitle>
        <DialogDescription className="mt-1 text-sm leading-6 text-muted-foreground">
          Snapshot-wide estimate for every actionable drift item. Search and
          status filters do not change this total.
        </DialogDescription>
      </div>

      {hasEstimate && summary ? (
        <>
          <section className="rounded-xl border border-primary/20 bg-[#edf6f3] p-4">
            <p className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              Estimated total
            </p>
            <p className="mt-1 text-3xl font-semibold tabular-nums text-primary">
              {formatUsdRange(
                summary.estimatedCostUsdMin,
                summary.estimatedCostUsdMax
              )}
            </p>
            <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-3">
              <CostFact
                label="Minimum"
                value={formatDetailedUsd(summary.estimatedCostUsdMin)}
              />
              <CostFact
                label="Maximum"
                value={formatDetailedUsd(summary.estimatedCostUsdMax)}
              />
              <CostFact
                label="Pricing as of"
                value={formatPricingDate(data?.pricingAsOf ?? '')}
              />
            </dl>
          </section>

          <section>
            <h3 className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              Cost breakdown
            </h3>
            {breakdown.length > 0 ? (
              <div className="mt-2 divide-y divide-border overflow-hidden rounded-xl border border-border">
                {breakdown.map((cost, index) => (
                  <CostBreakdownRow
                    key={`${cost.storageClass}-${cost.name}-${index}`}
                    cost={cost}
                  />
                ))}
              </div>
            ) : (
              <p className="mt-2 rounded-xl border border-border bg-white p-4 text-sm text-muted-foreground">
                No actionable drift items contribute to this estimate.
              </p>
            )}
          </section>

          <section className="grid gap-3 rounded-xl border border-border bg-[#fbfaf6] p-4 text-sm">
            <div>
              <p className="font-medium text-foreground">Minimum formula</p>
              <code className="mt-1 block whitespace-normal text-xs leading-5 text-muted-foreground">
                {summary.costFormula.minimum}
              </code>
            </div>
            <div>
              <p className="font-medium text-foreground">Maximum formula</p>
              <code className="mt-1 block whitespace-normal text-xs leading-5 text-muted-foreground">
                {summary.costFormula.maximum}
              </code>
            </div>
            {data?.pricingModel && (
              <p className="border-t border-border pt-3 text-xs text-muted-foreground">
                Pricing model: {data.pricingModel}
              </p>
            )}
          </section>

          {summary.warnings.length > 0 && (
            <section className="rounded-xl border border-amber-300 bg-amber-50 p-4">
              <h3 className="flex items-center gap-2 text-sm font-semibold text-amber-950">
                <AlertTriangle className="h-4 w-4" />
                Assumptions and exclusions
              </h3>
              <ul className="mt-2 grid gap-1.5 pl-5 text-xs leading-5 text-amber-950/80">
                {summary.warnings.map((warning) => (
                  <li key={warning} className="list-disc">
                    {warning}
                  </li>
                ))}
              </ul>
            </section>
          )}

          <section>
            <h3 className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              Official sources
            </h3>
            <div className="mt-2 flex flex-wrap gap-2">
              {(data?.pricingSources ?? []).map((source) => (
                <a
                  key={source.url}
                  href={source.url}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1.5 rounded-md border border-border bg-white px-3 py-2 text-xs font-medium text-primary hover:border-primary/50 hover:bg-[#f7fbfa] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {source.label}
                  <ExternalLink className="h-3 w-3" />
                </a>
              ))}
            </div>
          </section>
        </>
      ) : (
        <Alert className="border-destructive/40 bg-red-50 text-destructive">
          The cached snapshot does not contain a cost breakdown. Run a manual
          rescan before reviewing or approving physical moves.
        </Alert>
      )}
    </DialogContent>
  );
}

function CostBreakdownRow({ cost }: { cost: DriftCostItem }) {
  return (
    <div className="grid gap-3 bg-white p-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <p className="font-medium text-foreground">{cost.name}</p>
          {cost.storageClass && (
            <Badge variant="outline" className="text-[10px]">
              {cost.storageClass}
            </Badge>
          )}
        </div>
        <p className="mt-1 text-xs leading-5 text-muted-foreground">
          {cost.formula}
        </p>
        <p className="mt-1 font-mono text-[11px] leading-5 text-foreground/75">
          {formatCostNumber(cost.units)} {cost.unitLabel} ×{' '}
          {formatDetailedUsd(cost.rate)} / {cost.rateUnit.replace('USD/', '')}
        </p>
        <p className="mt-1 text-[11px] leading-5 text-muted-foreground">
          {cost.details}
        </p>
      </div>
      <div className="text-left sm:text-right">
        <p className="font-semibold tabular-nums">
          {formatDetailedUsdRange(cost.usdMin, cost.usdMax)}
        </p>
      </div>
    </div>
  );
}

function CostFact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-1 font-semibold tabular-nums">{value}</dd>
    </div>
  );
}

function formatCostNumber(value: number) {
  return new Intl.NumberFormat('en-US', {
    maximumFractionDigits: 4,
  }).format(value);
}

function formatPricingDate(value: string) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return formatDate(value);
  return `${Number(match[1])}年${Number(match[2])}月${Number(match[3])}日`;
}

function formatDetailedUsd(value: number) {
  if (!Number.isFinite(value) || value < 0) return formatUsd(0);
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: 6,
  }).format(value);
}

function formatDetailedUsdRange(min: number, max: number) {
  if (min === max) return formatDetailedUsd(min);
  return `${formatDetailedUsd(min)}–${formatDetailedUsd(max)}`;
}

function DriftTable({
  items,
  selected,
  canAct,
  allActionableSelected,
  onToggleAll,
  onToggle,
}: {
  items: DriftItem[];
  selected: Set<string>;
  canAct: boolean;
  allActionableSelected: boolean;
  onToggleAll: (checked: boolean) => void;
  onToggle: (path: string, checked: boolean) => void;
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

function DriftMobileCard({
  item,
  checked,
  canAct,
  onToggle,
}: {
  item: DriftItem;
  checked: boolean;
  canAct: boolean;
  onToggle: (path: string, checked: boolean) => void;
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

function ActionProgress({
  action,
  failedPaths,
  canRetry,
  retrying,
  onRetry,
  onDismiss,
}: {
  action: DriftAction;
  failedPaths: string[];
  canRetry: boolean;
  retrying: boolean;
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
            <Button variant="ghost" size="sm" onClick={onDismiss}>
              Dismiss
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

function PlanMetric({
  label,
  value,
  emphasize = false,
}: {
  label: string;
  value: string;
  emphasize?: boolean;
}) {
  return (
    <div className="rounded-lg border border-border bg-[#fbfaf6] p-3">
      <p className="text-[10px] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
        {label}
      </p>
      <p
        className={cn(
          'mt-1 text-sm font-semibold',
          emphasize && 'text-primary'
        )}
      >
        {value}
      </p>
    </div>
  );
}

function LoadingRows() {
  return (
    <div className="grid gap-3 p-4">
      {[0, 1, 2, 3].map((row) => (
        <div key={row} className="h-14 animate-pulse rounded-lg bg-muted/70" />
      ))}
    </div>
  );
}

function EmptyDriftState({
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
