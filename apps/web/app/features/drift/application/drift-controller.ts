import type {
  DriftAction,
  DriftItem,
  DriftPlan,
  DriftResponse,
  DriftScan,
} from '../domain/drift';
import {
  driftActionPaths,
  isActionableDriftItem,
  isDriftActionTerminal,
  markDriftActionRetrying,
  upsertDriftAction,
} from '../domain/drift-policy';
import { createDriftActionListResponseGuard } from './drift-action-list-guard';
import type { DriftGateway } from './drift-gateway';

const PAGE_SIZE = 50;
const SEARCH_DEBOUNCE_MS = 250;
const ACTION_POLL_MS = 1_500;
const ACTION_LIST_SYNC_MS = 30_000;
const SCAN_ACTIVE_POLL_MS = 2_000;
const SCAN_BACKGROUND_SYNC_MS = 30_000;

export type DriftScheduler = {
  setTimeout(callback: () => void, delayMs: number): unknown;
  clearTimeout(handle: unknown): void;
  setInterval(callback: () => void, intervalMs: number): unknown;
  clearInterval(handle: unknown): void;
};

type DriftLoadState = {
  data?: DriftResponse;
  loading: boolean;
  error?: string;
};

export type DriftControllerSnapshot = ReturnType<
  DriftController['buildSnapshot']
>;

export class DriftController {
  private active = false;
  private listeners = new Set<() => void>();
  private intervals = new Set<unknown>();
  private searchTimer?: unknown;
  private pendingLoadTimer?: unknown;
  private snapshot!: ReturnType<DriftController['buildSnapshot']>;
  private requestGeneration = 0;
  private scanRequestGeneration = 0;
  private actionListGuard = createDriftActionListResponseGuard();
  private pollingActions = false;
  private listingActions = false;
  private loadingScan = false;
  private appliedCompletedScanId?: string;

  private query = '';
  private debouncedQuery = '';
  private status = 'all';
  private offset = 0;
  private state: DriftLoadState = { loading: true };
  private selected = new Set<string>();
  private plan?: DriftPlan;
  private planning = false;
  private planError?: string;
  private costAcknowledged = false;
  private startingAction = false;
  private actions: DriftAction[] = [];
  private actionsLoading = true;
  private actionsError?: string;
  private retryingActionIds = new Set<string>();
  private dismissingActionIds = new Set<string>();
  private scan?: DriftScan;
  private startingScan = false;
  private scanError?: string;

  constructor(
    private readonly gateway: DriftGateway,
    private readonly scheduler: DriftScheduler
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
    this.scheduleDriftLoad();
    void this.loadScan();
    void this.loadActions();
    this.installPolling();
  };

  dispose = () => {
    this.active = false;
    this.requestGeneration += 1;
    this.scanRequestGeneration += 1;
    if (this.searchTimer !== undefined)
      this.scheduler.clearTimeout(this.searchTimer);
    if (this.pendingLoadTimer !== undefined)
      this.scheduler.clearTimeout(this.pendingLoadTimer);
    for (const interval of this.intervals) {
      this.scheduler.clearInterval(interval);
    }
    this.intervals.clear();
  };

  private emit() {
    this.snapshot = this.buildSnapshot();
    for (const listener of this.listeners) listener();
  }

  private installPolling() {
    const actions = this.scheduler.setInterval(() => {
      if (!this.canAct()) return;
      void this.pollActions();
    }, ACTION_POLL_MS);
    const actionList = this.scheduler.setInterval(() => {
      if (!this.canAct()) return;
      void this.loadActions(true);
    }, ACTION_LIST_SYNC_MS);
    const scan = this.scheduler.setInterval(() => {
      if (!this.canScan() || !this.scanRunning()) return;
      void this.loadScan();
    }, SCAN_ACTIVE_POLL_MS);
    const backgroundScan = this.scheduler.setInterval(() => {
      if (!this.canScan() || this.scanRunning()) return;
      void this.loadScan();
    }, SCAN_BACKGROUND_SYNC_MS);
    this.intervals.add(actions);
    this.intervals.add(actionList);
    this.intervals.add(scan);
    this.intervals.add(backgroundScan);
  }

  private scheduleDriftLoad() {
    if (!this.active || this.pendingLoadTimer !== undefined) return;
    this.pendingLoadTimer = this.scheduler.setTimeout(() => {
      this.pendingLoadTimer = undefined;
      void this.loadDrift();
    }, 0);
  }

  private loadDrift = async (refresh = false) => {
    const generation = ++this.requestGeneration;
    this.state = { ...this.state, loading: true, error: undefined };
    this.emit();
    try {
      const data = await this.gateway.getDrift({
        query: this.debouncedQuery,
        status: this.status,
        limit: PAGE_SIZE,
        offset: this.offset,
        refresh,
      });
      if (!this.active || generation !== this.requestGeneration) return;
      this.state = { data, loading: false };
      if (!this.canAct()) {
        this.actionListGuard.markMutation();
        this.actions = [];
        this.actionsLoading = false;
      } else if (this.actionsLoading) {
        void this.loadActions();
      }
      this.emit();
    } catch (error) {
      if (!this.active || generation !== this.requestGeneration) return;
      this.state = {
        ...this.state,
        loading: false,
        error:
          error instanceof Error
            ? error.message
            : 'Unable to scan storage drift',
      };
      this.emit();
    }
  };

  setQuery = (query: string) => {
    this.query = query;
    if (this.searchTimer !== undefined)
      this.scheduler.clearTimeout(this.searchTimer);
    this.searchTimer = this.scheduler.setTimeout(() => {
      this.searchTimer = undefined;
      this.debouncedQuery = this.query.trim();
      this.offset = 0;
      this.selected = new Set();
      this.emit();
      this.scheduleDriftLoad();
    }, SEARCH_DEBOUNCE_MS);
    this.emit();
  };

  setStatus = (status: string) => {
    this.status = status;
    this.selected = new Set();
    this.emit();
    this.scheduleDriftLoad();
  };

  setOffset = (offset: number) => {
    this.offset = offset;
    this.selected = new Set();
    this.emit();
    this.scheduleDriftLoad();
  };

  setSelected = (selected: Set<string>) => {
    this.selected = new Set(selected);
    this.emit();
  };

  toggleItem = (path: string, checked: boolean) => {
    const selected = new Set(this.selected);
    if (checked) selected.add(path);
    else selected.delete(path);
    this.selected = selected;
    this.emit();
  };

  toggleAll = (checked: boolean) => {
    const selected = new Set(this.selected);
    for (const item of this.actionableItems()) {
      if (checked) selected.add(item.logicPath);
      else selected.delete(item.logicPath);
    }
    this.selected = selected;
    this.emit();
  };

  setPlan = (plan: DriftPlan | undefined) => {
    this.plan = plan;
    this.emit();
  };

  setCostAcknowledged = (acknowledged: boolean) => {
    this.costAcknowledged = acknowledged;
    this.emit();
  };

  preparePlan = async (paths: string[]) => {
    if (paths.length === 0 || !this.canAct()) return;
    this.planning = true;
    this.planError = undefined;
    this.emit();
    try {
      this.plan = await this.gateway.createDriftPlan(paths);
      this.costAcknowledged = false;
    } catch (error) {
      this.planError =
        error instanceof Error ? error.message : 'Unable to create drift plan';
    } finally {
      this.planning = false;
      this.emit();
    }
  };

  startAction = async () => {
    if (!this.plan || !this.costAcknowledged) return;
    this.startingAction = true;
    this.planError = undefined;
    this.emit();
    try {
      const action = await this.gateway.createDriftAction(this.plan.planId);
      this.plan = undefined;
      this.selected = new Set();
      this.actionListGuard.markMutation();
      this.actions = upsertDriftAction(this.actions, action, true);
    } catch (error) {
      this.planError =
        error instanceof Error ? error.message : 'Unable to start drift action';
    } finally {
      this.startingAction = false;
      this.emit();
    }
  };

  retryAction = async (action: DriftAction) => {
    const id = action.id;
    const paths = driftActionPaths(action);
    if (!id || !action.idempotencyKey || paths.length === 0) return;
    this.retryingActionIds = new Set(this.retryingActionIds).add(id);
    this.planError = undefined;
    this.emit();
    try {
      const next = await this.gateway.createDriftAction(
        action.planId,
        action.idempotencyKey
      );
      this.actionListGuard.markMutation();
      this.actions = upsertDriftAction(
        this.actions,
        markDriftActionRetrying(next)
      );
    } catch (error) {
      this.planError =
        error instanceof Error ? error.message : 'Unable to retry drift action';
    } finally {
      const ids = new Set(this.retryingActionIds);
      ids.delete(id);
      this.retryingActionIds = ids;
      this.emit();
    }
  };

  dismissAction = async (action: DriftAction) => {
    const id = action.id;
    if (!id) return;
    this.dismissingActionIds = new Set(this.dismissingActionIds).add(id);
    this.actionsError = undefined;
    this.emit();
    try {
      await this.gateway.dismissDriftAction(id);
      this.actionListGuard.markMutation();
      this.actions = this.actions.filter((candidate) => candidate.id !== id);
    } catch (error) {
      this.actionsError =
        error instanceof Error
          ? error.message
          : 'Unable to dismiss drift action';
    } finally {
      const ids = new Set(this.dismissingActionIds);
      ids.delete(id);
      this.dismissingActionIds = ids;
      this.emit();
    }
  };

  loadActions = async (background = false) => {
    if (!this.active || this.listingActions || !this.canAct()) return;
    this.listingActions = true;
    const token = this.actionListGuard.beginRequest();
    if (!background) this.actionsLoading = true;
    this.emit();
    try {
      const actions = await this.gateway.getDriftActions();
      if (!this.active || !this.actionListGuard.isCurrent(token)) return;
      this.actions = actions;
      this.actionsError = undefined;
    } catch (error) {
      if (!this.active || !this.actionListGuard.isCurrent(token)) return;
      this.actionsError =
        error instanceof Error ? error.message : 'Unable to load drift actions';
    } finally {
      this.listingActions = false;
      if (!background && this.active) this.actionsLoading = false;
      if (this.active) this.emit();
    }
  };

  private pollActions = async () => {
    if (!this.active || this.pollingActions) return;
    const running = this.actions.filter(
      (action) => !isDriftActionTerminal(action.status)
    );
    if (running.length === 0) return;
    this.pollingActions = true;
    try {
      const results = await Promise.allSettled(
        running.map((action) => {
          const id = action.id;
          if (!id) throw new Error('Drift action is missing an ID');
          return this.gateway.getDriftAction(id);
        })
      );
      if (!this.active) return;
      const updates = results.flatMap((result) =>
        result.status === 'fulfilled' ? [result.value] : []
      );
      if (updates.length > 0) {
        this.actionListGuard.markMutation();
        this.actions = updates.reduce(
          (next, action) => upsertDriftAction(next, action),
          this.actions
        );
      }
      const failure = results.find(
        (result): result is PromiseRejectedResult =>
          result.status === 'rejected'
      );
      this.actionsError = failure
        ? failure.reason instanceof Error
          ? failure.reason.message
          : 'Unable to monitor drift actions'
        : undefined;
      this.emit();
    } finally {
      this.pollingActions = false;
    }
  };

  private loadScan = async () => {
    if (!this.active || this.loadingScan || !this.canScan()) return;
    this.loadingScan = true;
    const generation = this.scanRequestGeneration;
    try {
      const scan = await this.gateway.getCurrentDriftScan();
      if (!this.active || generation !== this.scanRequestGeneration) return;
      this.scan = scan;
      this.scanError = undefined;
      if (
        scan?.status === 'completed' &&
        this.appliedCompletedScanId !== scan.id
      ) {
        this.appliedCompletedScanId = scan.id;
        this.offset = 0;
        this.selected = new Set();
        this.scheduleDriftLoad();
      }
      this.emit();
    } catch (error) {
      if (!this.active || generation !== this.scanRequestGeneration) return;
      this.scanError =
        error instanceof Error ? error.message : 'Unable to load rescan status';
      this.emit();
    } finally {
      this.loadingScan = false;
    }
  };

  beginScan = async () => {
    if (this.startingScan || this.scanRunning() || !this.canScan()) return;
    this.startingScan = true;
    this.scanError = undefined;
    this.scanRequestGeneration += 1;
    this.emit();
    try {
      this.scan = await this.gateway.startDriftScan();
    } catch (error) {
      this.scanError =
        error instanceof Error ? error.message : 'Unable to start rescan';
    } finally {
      this.startingScan = false;
      this.emit();
    }
  };

  private actionableItems(): DriftItem[] {
    return (this.state.data?.items ?? []).filter(isActionableDriftItem);
  }

  private canAct() {
    const data = this.state.data;
    const storageIsGcs = data?.storageDriver.toLowerCase() === 'gcs';
    return Boolean(
      data && data.available && data.enabled && !data.readOnly && storageIsGcs
    );
  }

  private canScan() {
    return Boolean(this.state.data?.available);
  }

  private scanRunning() {
    return this.scan?.status === 'pending' || this.scan?.status === 'running';
  }

  buildSnapshot() {
    const data = this.state.data;
    const items = data?.items ?? [];
    const actionable = this.actionableItems();
    const storageIsGcs = data?.storageDriver.toLowerCase() === 'gcs';
    return {
      filters: {
        query: this.query,
        setQuery: this.setQuery,
        debouncedQuery: this.debouncedQuery,
        status: this.status,
        setStatus: this.setStatus,
        offset: this.offset,
        setOffset: this.setOffset,
      },
      drift: { state: this.state, data, items, pagination: data?.pagination },
      selection: {
        selected: this.selected,
        setSelected: this.setSelected,
        allActionableSelected:
          actionable.length > 0 &&
          actionable.every((item) => this.selected.has(item.logicPath)),
        toggleItem: this.toggleItem,
        toggleAll: this.toggleAll,
      },
      capability: {
        storageIsGcs,
        canAct: this.canAct(),
        canScan: this.canScan(),
      },
      plan: {
        plan: this.plan,
        setPlan: this.setPlan,
        planning: this.planning,
        planError: this.planError,
        costAcknowledged: this.costAcknowledged,
        setCostAcknowledged: this.setCostAcknowledged,
        startingAction: this.startingAction,
        preparePlan: this.preparePlan,
        startAction: this.startAction,
      },
      actions: {
        actions: this.actions,
        actionsLoading: this.actionsLoading,
        actionsError: this.actionsError,
        retryingActionIds: this.retryingActionIds,
        dismissingActionIds: this.dismissingActionIds,
        loadActions: this.loadActions,
        retryAction: this.retryAction,
        dismissAction: this.dismissAction,
      },
      scan: {
        scan: this.scan,
        startingScan: this.startingScan,
        scanError: this.scanError,
        scanRunning: this.scanRunning(),
        beginScan: this.beginScan,
      },
    };
  }
}
