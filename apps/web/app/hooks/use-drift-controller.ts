import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import type { DriftControllerDependencies } from '../features/drift/application/drift-controller-dependencies';
import { createDriftActionListResponseGuard } from '../features/drift/application/drift-action-list-guard';
import {
  driftActionPaths,
  isActionableDriftItem,
  isDriftActionTerminal,
  markDriftActionRetrying,
  upsertDriftAction,
} from '../features/drift/domain/drift-policy';
import type {
  DriftAction,
  DriftItem,
  DriftPlan,
  DriftResponse,
  DriftScan,
} from '../features/drift/domain/drift';

const PAGE_SIZE = 50;
const SEARCH_DEBOUNCE_MS = 250;
const ACTION_POLL_MS = 1500;
const ACTION_LIST_SYNC_MS = 30000;
const SCAN_ACTIVE_POLL_MS = 2000;
const SCAN_BACKGROUND_SYNC_MS = 30000;
const EMPTY_DRIFT_ITEMS: DriftItem[] = [];

export function scheduleDriftSearchCommit(
  query: string,
  commit: (query: string) => void,
  delay = SEARCH_DEBOUNCE_MS
) {
  const timeout = globalThis.setTimeout(() => commit(query.trim()), delay);
  return () => globalThis.clearTimeout(timeout);
}

export function scheduleDriftPoll(poll: () => void, intervalMs: number) {
  const interval = globalThis.setInterval(poll, intervalMs);
  return () => globalThis.clearInterval(interval);
}

export function createDriftControllerRequestGuard() {
  let active = true;
  let generation = 0;

  return {
    activate() {
      active = true;
    },
    dispose() {
      active = false;
      generation += 1;
    },
    isActive() {
      return active;
    },
    beginRequest() {
      generation += 1;
      return generation;
    },
    isCurrent(requestGeneration: number) {
      return active && requestGeneration === generation;
    },
  };
}

type LoadState = {
  data?: DriftResponse;
  loading: boolean;
  error?: string;
};

export function useDriftController(dependencies: DriftControllerDependencies) {
  const {
    createDriftAction,
    createDriftPlan,
    dismissDriftAction,
    getCurrentDriftScan,
    getDrift,
    getDriftAction,
    getDriftActions,
    startDriftScan,
  } = dependencies;
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
  const controllerRequestGuardRef = useRef(createDriftControllerRequestGuard());
  const actionsRef = useRef<DriftAction[]>([]);
  const pollingActionsRef = useRef(false);
  const listingActionsRef = useRef(false);
  const actionListGuardRef = useRef(createDriftActionListResponseGuard());
  const scanRequestRef = useRef(0);
  const loadingScanRef = useRef(false);
  const appliedCompletedScanRef = useRef<string | undefined>(undefined);

  const loadDrift = useCallback(
    async (nextOffset = offset, refresh = false) => {
      const requestGeneration =
        controllerRequestGuardRef.current.beginRequest();
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
        if (!controllerRequestGuardRef.current.isCurrent(requestGeneration))
          return;
        setState({ data, loading: false });
      } catch (error) {
        if (!controllerRequestGuardRef.current.isCurrent(requestGeneration))
          return;
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
    [debouncedQuery, getDrift, offset, status]
  );

  useEffect(() => {
    return scheduleDriftSearchCommit(query, (nextQuery) => {
      setOffset(0);
      setDebouncedQuery(nextQuery);
    });
  }, [query]);

  useEffect(() => {
    void loadDrift();
  }, [loadDrift]);

  useEffect(() => {
    setSelected(new Set());
  }, [debouncedQuery, offset, status]);

  useEffect(() => {
    const requestGuard = controllerRequestGuardRef.current;
    requestGuard.activate();
    return () => {
      requestGuard.dispose();
    };
  }, [getDriftActions]);

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

  const loadActions = useCallback(
    async (background = false) => {
      if (listingActionsRef.current) return;
      listingActionsRef.current = true;
      const requestToken = actionListGuardRef.current.beginRequest();
      if (!background) setActionsLoading(true);
      try {
        const next = await getDriftActions();
        if (
          !controllerRequestGuardRef.current.isActive() ||
          !actionListGuardRef.current.isCurrent(requestToken)
        ) {
          return;
        }
        setActions(next);
        setActionsError(undefined);
      } catch (error) {
        if (
          !controllerRequestGuardRef.current.isActive() ||
          !actionListGuardRef.current.isCurrent(requestToken)
        ) {
          return;
        }
        setActionsError(
          error instanceof Error
            ? error.message
            : 'Unable to load drift actions'
        );
      } finally {
        listingActionsRef.current = false;
        if (!background && controllerRequestGuardRef.current.isActive()) {
          setActionsLoading(false);
        }
      }
    },
    [getDriftActions]
  );

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
    return scheduleDriftPoll(() => {
      void loadActions(true);
    }, ACTION_LIST_SYNC_MS);
  }, [canAct, loadActions]);

  useEffect(() => {
    const pollActions = async () => {
      if (
        !controllerRequestGuardRef.current.isActive() ||
        pollingActionsRef.current
      ) {
        return;
      }
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
        if (!controllerRequestGuardRef.current.isActive()) return;
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

    return scheduleDriftPoll(() => {
      void pollActions();
    }, ACTION_POLL_MS);
  }, [getDriftAction]);

  const loadScan = useCallback(async () => {
    if (loadingScanRef.current) return;
    loadingScanRef.current = true;
    const requestId = scanRequestRef.current;
    try {
      const next = await getCurrentDriftScan();
      if (
        !controllerRequestGuardRef.current.isActive() ||
        requestId !== scanRequestRef.current
      ) {
        return;
      }
      setScan(next);
      setScanError(undefined);
    } catch (error) {
      if (
        !controllerRequestGuardRef.current.isActive() ||
        requestId !== scanRequestRef.current
      ) {
        return;
      }
      setScanError(
        error instanceof Error ? error.message : 'Unable to load rescan status'
      );
    } finally {
      loadingScanRef.current = false;
    }
  }, [getCurrentDriftScan]);

  const beginScan = useCallback(async () => {
    if (startingScan || scanRunning) return;
    setStartingScan(true);
    setScanError(undefined);
    scanRequestRef.current += 1;
    try {
      const next = await startDriftScan();
      if (!controllerRequestGuardRef.current.isActive()) return;
      setScan(next);
    } catch (error) {
      if (!controllerRequestGuardRef.current.isActive()) return;
      setScanError(
        error instanceof Error ? error.message : 'Unable to start rescan'
      );
    } finally {
      if (controllerRequestGuardRef.current.isActive()) setStartingScan(false);
    }
  }, [scanRunning, startDriftScan, startingScan]);

  useEffect(() => {
    if (!canScan) return;
    void loadScan();
    return scheduleDriftPoll(
      () => void loadScan(),
      scanRunning ? SCAN_ACTIVE_POLL_MS : SCAN_BACKGROUND_SYNC_MS
    );
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

  const filters = {
    query,
    setQuery,
    debouncedQuery,
    status,
    setStatus,
    offset,
    setOffset,
  };
  const drift = {
    state,
    data,
    items,
    pagination,
  };
  const selection = {
    selected,
    setSelected,
    allActionableSelected,
    toggleItem,
    toggleAll,
  };
  const capability = {
    storageIsGcs,
    canAct,
    canScan,
  };
  const planController = {
    plan,
    setPlan,
    planning,
    planError,
    costAcknowledged,
    setCostAcknowledged,
    startingAction,
    preparePlan,
    startAction,
  };
  const actionController = {
    actions,
    actionsLoading,
    actionsError,
    retryingActionIds,
    dismissingActionIds,
    loadActions,
    retryAction,
    dismissAction,
  };
  const scanController = {
    scan,
    startingScan,
    scanError,
    scanRunning,
    beginScan,
  };

  return {
    filters,
    drift,
    selection,
    capability,
    plan: planController,
    actions: actionController,
    scan: scanController,
  };
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
