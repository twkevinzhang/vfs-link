import type { Pagination } from '../../../shared/kernel/pagination';

export type DriftItem = {
  logicPath: string;
  currentKey: string;
  targetKey: string;
  status: string;
  size: number;
  storageClass: string;
  generation: string | number;
  estimatedCostUsdMin: number;
  estimatedCostUsdMax: number;
  actionable?: boolean;
  scope?: string;
  method?: string;
  error?: string;
};

export type DriftCostItem = {
  name: string;
  storageClass?: string;
  units: number;
  unitLabel: string;
  rate: number;
  rateUnit: string;
  formula: string;
  usdMin: number;
  usdMax: number;
  details: string;
};
export type DriftCostFormula = { minimum: string; maximum: string };
export type DriftPricingSource = { label: string; url: string };
export type DriftSummary = {
  total: number;
  aligned: number;
  drifted: number;
  missing: number;
  failed: number;
  totalBytes: number;
  estimatedCostUsdMin: number;
  estimatedCostUsdMax: number;
  costBreakdown: DriftCostItem[];
  costFormula: DriftCostFormula;
  warnings: string[];
};
export type DriftResponse = {
  available?: boolean;
  enabled?: boolean;
  readOnly?: boolean;
  reason?: string;
  storageDriver?: string;
  summary: DriftSummary;
  items: DriftItem[];
  pagination: Pagination;
  pricingAsOf: string;
  pricingModel: string;
  pricingSources: DriftPricingSource[];
  generatedAt: string;
};
export type DriftPlanItem = DriftItem & { eligible?: boolean; reason?: string };
export type DriftPlan = {
  planId: string;
  paths: string[];
  items: DriftPlanItem[];
  totalBytes: number;
  estimatedCostUsdMin: number;
  estimatedCostUsdMax: number;
  pricingAsOf: string;
  method?: string;
  costBreakdown?: DriftCostItem[];
  warnings?: string[];
  expiresAt?: string;
};
export type DriftActionResult = {
  logicPath: string;
  status: string;
  error?: string;
};
export type DriftAction = {
  id: string;
  actionId?: string;
  idempotencyKey?: string;
  planId: string;
  status: string;
  progress: number;
  total: number;
  succeeded: number;
  failed: number;
  failedPaths?: string[];
  results?: DriftActionResult[];
  error?: string;
  createdAt?: string;
  updatedAt?: string;
};
export type DriftActionsResponse = { actions: DriftAction[] };
export type DriftScan = {
  id: string;
  status: 'pending' | 'running' | 'completed' | 'failed';
  phase: 'queued' | 'metadata' | 'objects' | 'saving' | 'completed' | 'failed';
  error?: string;
  createdAt: string;
  updatedAt: string;
  completedAt?: string;
};
export type DriftCurrentScanResponse = { scan?: DriftScan | null };
