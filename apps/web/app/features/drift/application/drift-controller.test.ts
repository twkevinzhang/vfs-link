import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DriftResponse } from '../domain/drift';
import { DriftController, type DriftScheduler } from './drift-controller';
import type { DriftGateway } from './drift-gateway';

const scheduler: DriftScheduler = {
  setTimeout: (callback, delay) => globalThis.setTimeout(callback, delay),
  clearTimeout: (handle) => globalThis.clearTimeout(handle as number),
  setInterval: (callback, delay) => globalThis.setInterval(callback, delay),
  clearInterval: (handle) => globalThis.clearInterval(handle as number),
};

function emptyResponse(query = ''): DriftResponse {
  return {
    available: true,
    enabled: true,
    readOnly: false,
    storageDriver: 'gcs',
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
      limit: 50,
      offset: 0,
      total: 0,
      query,
      hasNext: false,
      hasPrev: false,
    },
    pricingAsOf: '',
    pricingModel: '',
    pricingSources: [],
    generatedAt: '',
  };
}

function createGateway(getDrift = vi.fn().mockResolvedValue(emptyResponse())) {
  return {
    getDrift,
    createDriftPlan: vi.fn(),
    createDriftAction: vi.fn(),
    getDriftAction: vi.fn(),
    getDriftActions: vi.fn().mockResolvedValue([]),
    dismissDriftAction: vi.fn(),
    getCurrentDriftScan: vi.fn().mockResolvedValue(undefined),
    startDriftScan: vi.fn(),
  } satisfies DriftGateway;
}

afterEach(() => {
  vi.useRealTimers();
});

describe('DriftController', () => {
  it('owns debounced search and loads only the trimmed latest query', async () => {
    vi.useFakeTimers();
    const getDrift = vi
      .fn()
      .mockResolvedValueOnce(emptyResponse())
      .mockResolvedValueOnce(emptyResponse('latest'));
    const controller = new DriftController(createGateway(getDrift), scheduler);
    controller.start();
    await vi.advanceTimersByTimeAsync(1);

    controller.getSnapshot().filters.setQuery(' first ');
    await vi.advanceTimersByTimeAsync(100);
    controller.getSnapshot().filters.setQuery(' latest ');
    await vi.advanceTimersByTimeAsync(249);
    expect(getDrift).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(getDrift).toHaveBeenLastCalledWith(
      expect.objectContaining({ query: 'latest', offset: 0 })
    );
    controller.dispose();
  });

  it('does not publish a response after disposal', async () => {
    vi.useFakeTimers();
    let resolve!: (response: DriftResponse) => void;
    const pending = new Promise<DriftResponse>((nextResolve) => {
      resolve = nextResolve;
    });
    const controller = new DriftController(
      createGateway(vi.fn().mockReturnValue(pending)),
      scheduler
    );
    controller.start();
    await vi.advanceTimersByTimeAsync(0);
    controller.dispose();
    resolve(emptyResponse());
    await pending;

    expect(controller.getSnapshot().drift.data).toBeUndefined();
  });
});
