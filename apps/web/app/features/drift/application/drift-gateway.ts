import type {
  DriftAction,
  DriftPlan,
  DriftResponse,
  DriftScan,
} from '../domain/drift';

export type GetDriftOptions = {
  query?: string;
  status?: string;
  limit?: number;
  offset?: number;
  refresh?: boolean;
};

export type DriftGateway = {
  getDrift(options?: GetDriftOptions): Promise<DriftResponse>;
  createDriftPlan(paths: string[]): Promise<DriftPlan>;
  createDriftAction(
    planId: string,
    existingIdempotencyKey?: string
  ): Promise<DriftAction>;
  getDriftAction(id: string): Promise<DriftAction>;
  getDriftActions(): Promise<DriftAction[]>;
  dismissDriftAction(id: string): Promise<void>;
  getCurrentDriftScan(): Promise<DriftScan | undefined>;
  startDriftScan(): Promise<DriftScan>;
};
