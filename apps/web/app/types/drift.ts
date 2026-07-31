import type { Pagination } from './files';

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

export type DriftSummary = {
  total: number;
  aligned: number;
  drifted: number;
  missing: number;
  failed: number;
  totalBytes: number;
  estimatedCostUsdMin: number;
  estimatedCostUsdMax: number;
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
  generatedAt: string;
};

export type DriftPlanItem = DriftItem & {
  eligible?: boolean;
  reason?: string;
};

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

export type DriftCostItem = {
  name: string;
  units: number;
  usdMin: number;
  usdMax: number;
  details: string;
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
