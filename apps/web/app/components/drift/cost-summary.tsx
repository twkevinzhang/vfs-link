import {
  AlertTriangle,
  ArrowRight,
  CheckCircle2,
  CircleDollarSign,
  CloudCog,
  DatabaseZap,
  ExternalLink,
} from 'lucide-react';

import {
  formatUsd,
  formatUsdRange,
} from '../../features/drift/presentation/drift-formatters';
import { formatBytes } from '../../lib/format';
import { cn } from '../../lib/utils';
import type {
  DriftCostItem,
  DriftResponse,
} from '../../features/drift/domain/drift';
import { Alert } from '../ui/alert';
import { Badge } from '../ui/badge';
import { Card } from '../ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from '../ui/dialog';
import { formatPricingDate } from './format-pricing-date';

export function DriftSummary({
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
                value={formatPricingDate(data.pricingAsOf)}
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
            {data.pricingModel && (
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
              {data.pricingSources.map((source) => (
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
