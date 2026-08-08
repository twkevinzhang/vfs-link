import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  CircleDollarSign,
  DatabaseZap,
  LoaderCircle,
  RefreshCcw,
  Search,
  ShieldAlert,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, type MetaFunction } from 'react-router';

import { Alert } from '../components/ui/alert';
import { DriftSummary } from '../components/drift/cost-summary';
import {
  DriftLoadingRows,
  DriftMobileCard,
  DriftTable,
  EmptyDriftState,
} from '../components/drift/drift-list';
import {
  DriftActionProgress,
  DriftScanProgress,
} from '../components/drift/progress';
import { formatPricingDate } from '../components/drift/format-pricing-date';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '../components/ui/alert-dialog';
import { Button } from '../components/ui/button';
import { Checkbox } from '../components/ui/checkbox';
import { Input } from '../components/ui/input';
import {
  createDriftAction,
  createDriftPlan,
  dismissDriftAction,
  getCurrentDriftScan,
  getDrift,
  getDriftAction,
  getDriftActions,
  startDriftScan,
} from '../lib/api';
import {
  createDriftActionListResponseGuard,
  driftActionFailedPaths,
  driftActionPaths,
  driftMethodLabel,
  formatUsdRange,
  isActionableDriftItem,
  isDriftActionTerminal,
  markDriftActionRetrying,
  upsertDriftAction,
} from '../lib/drift';
import { FILES_ROUTE } from '../lib/file-route';
import { formatBytes, formatDate } from '../lib/format';
import { cn } from '../lib/utils';
import type {
  DriftAction,
  DriftItem,
  DriftPlan,
  DriftResponse,
  DriftScan,
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
const ACTION_LIST_SYNC_MS = 30000;
const SCAN_ACTIVE_POLL_MS = 2000;
const SCAN_BACKGROUND_SYNC_MS = 30000;
const EMPTY_DRIFT_ITEMS: DriftItem[] = [];

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
  const [actions, setActions] = useState<DriftAction[]>([]);
  const [actionsLoading, setActionsLoading] = useState(true);
  const [actionsError, setActionsError] = useState<string>();
  const [retryingActionIds, setRetryingActionIds] = useState<Set<string>>(
    new Set()
  );
  const [dismissingActionIds, setDismissingActionIds] = useState<Set<string>>(
    new Set()
  );
  const [startingAction, setStartingAction] = useState(false);
  const [scan, setScan] = useState<DriftScan>();
  const [startingScan, setStartingScan] = useState(false);
  const [scanError, setScanError] = useState<string>();
  const requestRef = useRef(0);
  const disposedRef = useRef(false);
  const actionsRef = useRef<DriftAction[]>([]);
  const pollingActionsRef = useRef(false);
  const listingActionsRef = useRef(false);
  const actionListGuardRef = useRef(createDriftActionListResponseGuard());
  const scanRequestRef = useRef(0);
  const loadingScanRef = useRef(false);
  const appliedCompletedScanRef = useRef<string | undefined>(undefined);

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

  useEffect(() => {
    disposedRef.current = false;
    return () => {
      disposedRef.current = true;
    };
  }, []);

  const data = state.data;
  const driftCapabilityKnown = Boolean(data);
  const items = data?.items ?? EMPTY_DRIFT_ITEMS;
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
  const canScan = Boolean(data && data.available !== false);
  const scanRunning = scan?.status === 'pending' || scan?.status === 'running';
  const actionableItems = useMemo(
    () => items.filter(isActionableDriftItem),
    [items]
  );
  const allActionableSelected =
    actionableItems.length > 0 &&
    actionableItems.every((item) => selected.has(item.logicPath));

  const loadActions = useCallback(async (background = false) => {
    if (listingActionsRef.current) return;
    listingActionsRef.current = true;
    const requestToken = actionListGuardRef.current.beginRequest();
    if (!background) setActionsLoading(true);
    try {
      const next = await getDriftActions();
      if (
        disposedRef.current ||
        !actionListGuardRef.current.isCurrent(requestToken)
      ) {
        return;
      }
      setActions(next);
      setActionsError(undefined);
    } catch (error) {
      if (
        disposedRef.current ||
        !actionListGuardRef.current.isCurrent(requestToken)
      ) {
        return;
      }
      setActionsError(
        error instanceof Error ? error.message : 'Unable to load drift actions'
      );
    } finally {
      listingActionsRef.current = false;
      if (!background && !disposedRef.current) setActionsLoading(false);
    }
  }, []);

  useEffect(() => {
    actionsRef.current = actions;
  }, [actions]);

  useEffect(() => {
    if (!canAct) {
      if (driftCapabilityKnown) {
        actionListGuardRef.current.markMutation();
        setActions([]);
        setActionsLoading(false);
      }
      return;
    }
    void loadActions();
  }, [canAct, driftCapabilityKnown, loadActions]);

  useEffect(() => {
    if (!canAct) return;
    const interval = window.setInterval(() => {
      void loadActions(true);
    }, ACTION_LIST_SYNC_MS);
    return () => window.clearInterval(interval);
  }, [canAct, loadActions]);

  useEffect(() => {
    const pollActions = async () => {
      if (disposedRef.current || pollingActionsRef.current) return;
      const runningActions = actionsRef.current.filter(
        (action) => !isDriftActionTerminal(action.status)
      );
      if (runningActions.length === 0) return;

      pollingActionsRef.current = true;
      try {
        const results = await Promise.allSettled(
          runningActions.map((action) => {
            const id = action.id || action.actionId;
            if (!id) throw new Error('Drift action is missing an ID');
            return getDriftAction(id, driftActionPaths(action));
          })
        );
        if (disposedRef.current) return;
        const updates = results.flatMap((result) =>
          result.status === 'fulfilled' ? [result.value] : []
        );
        if (updates.length > 0) {
          actionListGuardRef.current.markMutation();
          setActions((previous) =>
            updates.reduce(
              (next, action) => upsertDriftAction(next, action),
              previous
            )
          );
        }
        const failure = results.find(
          (result): result is PromiseRejectedResult =>
            result.status === 'rejected'
        );
        setActionsError(
          failure
            ? failure.reason instanceof Error
              ? failure.reason.message
              : 'Unable to monitor drift actions'
            : undefined
        );
      } finally {
        pollingActionsRef.current = false;
      }
    };

    const interval = window.setInterval(() => {
      void pollActions();
    }, ACTION_POLL_MS);
    return () => window.clearInterval(interval);
  }, []);

  const loadScan = useCallback(async () => {
    if (loadingScanRef.current) return;
    loadingScanRef.current = true;
    const requestId = scanRequestRef.current;
    try {
      const next = await getCurrentDriftScan();
      if (disposedRef.current || requestId !== scanRequestRef.current) return;
      setScan(next);
      setScanError(undefined);
    } catch (error) {
      if (disposedRef.current || requestId !== scanRequestRef.current) return;
      setScanError(
        error instanceof Error ? error.message : 'Unable to load rescan status'
      );
    } finally {
      loadingScanRef.current = false;
    }
  }, []);

  const beginScan = useCallback(async () => {
    if (startingScan || scanRunning) return;
    setStartingScan(true);
    setScanError(undefined);
    scanRequestRef.current += 1;
    try {
      const next = await startDriftScan();
      if (disposedRef.current) return;
      setScan(next);
    } catch (error) {
      if (disposedRef.current) return;
      setScanError(
        error instanceof Error ? error.message : 'Unable to start rescan'
      );
    } finally {
      if (!disposedRef.current) setStartingScan(false);
    }
  }, [scanRunning, startingScan]);

  useEffect(() => {
    if (!canScan) return;
    void loadScan();
    const interval = window.setInterval(
      () => void loadScan(),
      scanRunning ? SCAN_ACTIVE_POLL_MS : SCAN_BACKGROUND_SYNC_MS
    );
    return () => window.clearInterval(interval);
  }, [canScan, loadScan, scanRunning]);

  useEffect(() => {
    if (
      scan?.status === 'completed' &&
      appliedCompletedScanRef.current !== scan.id
    ) {
      appliedCompletedScanRef.current = scan.id;
      setOffset(0);
      setSelected(new Set());
      void loadDrift(0, false);
    }
  }, [loadDrift, scan]);

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
      actionListGuardRef.current.markMutation();
      setActions((previous) => upsertDriftAction(previous, action, true));
    } catch (error) {
      setPlanError(
        error instanceof Error ? error.message : 'Unable to start drift action'
      );
    } finally {
      setStartingAction(false);
    }
  };

  const retryAction = async (action: DriftAction) => {
    const id = action.id || action.actionId;
    const paths = driftActionPaths(action);
    if (!id || !action.idempotencyKey || paths.length === 0) return;
    setRetryingActionIds((previous) => new Set(previous).add(id));
    setPlanError(undefined);
    try {
      const next = await createDriftAction(
        action.planId,
        paths,
        action.idempotencyKey
      );
      actionListGuardRef.current.markMutation();
      setActions((previous) =>
        upsertDriftAction(previous, markDriftActionRetrying(next))
      );
    } catch (error) {
      setPlanError(
        error instanceof Error ? error.message : 'Unable to retry drift action'
      );
    } finally {
      setRetryingActionIds((previous) => {
        const next = new Set(previous);
        next.delete(id);
        return next;
      });
    }
  };

  const dismissAction = async (action: DriftAction) => {
    const id = action.id || action.actionId;
    if (!id) return;
    setDismissingActionIds((previous) => new Set(previous).add(id));
    setActionsError(undefined);
    try {
      await dismissDriftAction(id);
      actionListGuardRef.current.markMutation();
      setActions((previous) =>
        previous.filter(
          (candidate) => (candidate.id || candidate.actionId) !== id
        )
      );
    } catch (error) {
      setActionsError(
        error instanceof Error
          ? error.message
          : 'Unable to dismiss drift action'
      );
    } finally {
      setDismissingActionIds((previous) => {
        const next = new Set(previous);
        next.delete(id);
        return next;
      });
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
                onClick={() => void beginScan()}
                disabled={startingScan || scanRunning || !canScan}
              >
                <RefreshCcw
                  className={cn(
                    'h-4 w-4',
                    (startingScan || scanRunning) && 'animate-spin'
                  )}
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

        {(state.error || planError || actionsError || scanError) && (
          <Alert className="border-destructive/30 bg-red-50 text-destructive">
            <div className="flex items-start gap-3">
              <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0" />
              <div>
                <p className="font-semibold">Drift operation unavailable</p>
                <p className="mt-1 text-sm text-foreground">
                  {planError || state.error || actionsError || scanError}
                </p>
                {actionsError && (
                  <Button
                    variant="outline"
                    size="sm"
                    className="mt-3"
                    disabled={actionsLoading}
                    onClick={() => void loadActions()}
                  >
                    {actionsLoading && (
                      <LoaderCircle className="h-4 w-4 animate-spin" />
                    )}
                    Retry action sync
                  </Button>
                )}
              </div>
            </div>
          </Alert>
        )}

        {scan && (
          <DriftScanProgress
            scan={scan}
            retrying={startingScan}
            onRetry={() => void beginScan()}
          />
        )}

        {actions.length > 0 && (
          <section className="grid gap-3" aria-label="Physical move actions">
            {actions.map((action) => {
              const id = (action.id || action.actionId) ?? '';
              const failedPaths = driftActionFailedPaths(action);
              return (
                <DriftActionProgress
                  key={id}
                  action={action}
                  failedPaths={failedPaths}
                  canRetry={
                    canAct &&
                    Boolean(action.idempotencyKey) &&
                    driftActionPaths(action).length > 0
                  }
                  retrying={retryingActionIds.has(id)}
                  dismissing={dismissingActionIds.has(id)}
                  onRetry={() => void retryAction(action)}
                  onDismiss={() => void dismissAction(action)}
                />
              );
            })}
          </section>
        )}

        <DriftSummary data={data} loading={state.loading && !data} />

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
            <DriftLoadingRows />
          ) : items.length === 0 ? (
            <EmptyDriftState
              hasFilter={Boolean(debouncedQuery || status !== 'all')}
              hasSnapshot={Boolean(data?.generatedAt)}
              scanning={startingScan || scanRunning}
              onRescan={() => void beginScan()}
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
