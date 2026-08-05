import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  createDriftAction,
  createDriftPlan,
  dismissDriftAction,
  getCurrentDriftScan,
  getDrift,
  getDriftActions,
  startDriftScan,
} from './api';

const emptyResponse = {
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
    costFormula: {
      minimum: 'Data retrieval + Class A operations + Class B operations',
      maximum: 'Minimum estimate + early deletion upper bound',
    },
    warnings: [],
  },
  items: [],
  pagination: {
    limit: 50,
    offset: 0,
    total: 0,
    query: '',
    hasNext: false,
    hasPrev: false,
  },
  pricingAsOf: '2026-08-01',
  pricingModel: 'Google Cloud Storage regional flat-namespace list pricing',
  pricingSources: [
    {
      label: 'Google Cloud Storage pricing',
      url: 'https://cloud.google.com/storage/pricing',
    },
  ],
  generatedAt: '2026-08-01T00:00:00Z',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('drift API query contract', () => {
  it('starts and restores a server-authoritative rescan job', async () => {
    const scan = {
      id: 'scan-one',
      status: 'running',
      phase: 'objects',
      createdAt: '2026-08-05T09:00:00Z',
      updatedAt: '2026-08-05T09:01:00Z',
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(scan), {
          status: 202,
          headers: { 'Content-Type': 'application/json' },
        })
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ scan }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
    vi.stubGlobal('fetch', fetchMock);

    await expect(startDriftScan()).resolves.toEqual(scan);
    await expect(getCurrentDriftScan()).resolves.toEqual(scan);
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/drift/scans',
      expect.objectContaining({ method: 'POST', body: '{}' })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/drift/scans/current',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
  });

  it('returns no current rescan before the first job exists', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ scan: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    );

    await expect(getCurrentDriftScan()).resolves.toBeUndefined();
  });
  it('reads the cached snapshot without triggering a refresh by default', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(emptyResponse), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    );
    vi.stubGlobal('fetch', fetchMock);

    await getDrift({ query: 'archive', status: 'shared_object', limit: 50 });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/drift?q=archive&status=shared_object&limit=50',
      expect.any(Object)
    );
    expect(fetchMock.mock.calls[0][0]).not.toContain('refresh');
  });

  it('adds refresh=true only for an explicit manual rescan', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(emptyResponse), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    );
    vi.stubGlobal('fetch', fetchMock);

    await getDrift({ limit: 50, offset: 0, refresh: true });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/drift?limit=50&offset=0&refresh=true',
      expect.any(Object)
    );
  });

  it('preserves the snapshot cost formula, calculation rows, and sources', async () => {
    const response = {
      ...emptyResponse,
      summary: {
        ...emptyResponse.summary,
        estimatedCostUsdMin: 0.1,
        estimatedCostUsdMax: 0.2,
        costBreakdown: [
          {
            name: 'Data retrieval',
            storageClass: 'ARCHIVE',
            units: 2,
            unitLabel: 'GiB',
            rate: 0.05,
            rateUnit: 'USD/GiB',
            formula: 'stored GiB × retrieval rate',
            usdMin: 0.1,
            usdMax: 0.1,
            details: 'Retrieval estimate.',
          },
        ],
        warnings: ['Not a bill.'],
      },
    };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(response), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    );

    const result = await getDrift({ limit: 50 });

    expect(result.summary.costBreakdown[0]).toMatchObject({
      storageClass: 'ARCHIVE',
      units: 2,
      rate: 0.05,
    });
    expect(result.summary.costFormula.minimum).toContain('Data retrieval');
    expect(result.pricingSources[0].url).toBe(
      'https://cloud.google.com/storage/pricing'
    );
  });

  it('normalizes the backend snapshot envelope for the management UI', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            snapshotStatus: 'ready',
            scanning: false,
            entries: [
              {
                logicPath: 'archive/report.pdf',
                physicalHash: 'legacy-key',
                targetKey: 'archive/report.pdf',
                size: 1024,
                scope: 'active',
                status: 'shared_object',
                actionable: true,
                object: {
                  name: 'legacy-key',
                  size: 1024,
                  generation: 42,
                  storageClass: 'ARCHIVE',
                },
              },
            ],
            counts: { shared_object: 1 },
            generatedAt: '2026-08-01T00:00:00Z',
            objectRoot: 'gs://prod-data',
            offset: 0,
            limit: 50,
            total: 1,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      )
    );

    const result = await getDrift({ limit: 50 });

    expect(result.storageDriver).toBe('gcs');
    expect(result.summary.drifted).toBe(1);
    expect(result.items[0]).toMatchObject({
      logicPath: 'archive/report.pdf',
      currentKey: 'legacy-key',
      targetKey: 'archive/report.pdf',
      generation: '42',
      actionable: true,
    });
  });

  it('supplies an idempotency key and maps a failed action to its path', async () => {
    const fetchMock = vi.fn().mockImplementation(
      async () =>
        new Response(
          JSON.stringify({
            id: 'action-1',
            planId: 'plan-1',
            status: 'failed',
            checkpoint: 'pending',
            entryIndex: 1,
            error: 'generation changed',
          }),
          { status: 202, headers: { 'Content-Type': 'application/json' } }
        )
    );
    vi.stubGlobal('fetch', fetchMock);

    const action = await createDriftAction('plan-1', ['/ok', '/failed']);
    const request = fetchMock.mock.calls[0][1] as RequestInit;
    const body = JSON.parse(String(request.body)) as Record<string, string>;

    expect(body.planId).toBe('plan-1');
    expect(body.idempotencyKey).toEqual(expect.any(String));
    expect(action.idempotencyKey).toBe(body.idempotencyKey);
    expect(action.failedPaths).toEqual(['/failed']);
    expect(action.progress).toBe(1);

    await createDriftAction(
      'plan-1',
      ['/ok', '/failed'],
      action.idempotencyKey
    );
    const retryRequest = fetchMock.mock.calls[1][1] as RequestInit;
    const retryBody = JSON.parse(String(retryRequest.body)) as Record<
      string,
      string
    >;
    expect(retryBody.idempotencyKey).toBe(body.idempotencyKey);
  });

  it('derives plan paths from normalized plan items when omitted', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            planId: 'plan-1',
            items: [
              {
                logicPath: 'report.pdf',
                currentKey: 'old',
                targetKey: 'report.pdf',
                status: 'planned',
                size: 10,
                storageClass: 'STANDARD',
                generation: 42,
                estimatedCostUsdMin: 0,
                estimatedCostUsdMax: 0,
                method: 'copy_verify_update_delete',
              },
            ],
            totalBytes: 10,
            estimatedCostUsdMin: 0.001,
            estimatedCostUsdMax: 0.002,
            pricingAsOf: '2026-08-01',
            method: 'copy_verify_update_conditional_delete',
          }),
          { status: 201, headers: { 'Content-Type': 'application/json' } }
        )
      )
    );

    const plan = await createDriftPlan(['report.pdf']);

    expect(plan.paths).toEqual(['report.pdf']);
  });

  it('lists server-authoritative actions and preserves result paths', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          actions: [
            {
              id: 'action-2',
              planId: 'plan-2',
              idempotencyKey: 'key-2',
              status: 'running',
              progress: 1,
              total: 2,
              succeeded: 1,
              failed: 0,
              results: [
                { logicPath: '/done', status: 'completed' },
                { logicPath: '/next', status: 'pending' },
              ],
            },
          ],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      )
    );
    vi.stubGlobal('fetch', fetchMock);

    const actions = await getDriftActions();

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/drift/actions?all=true',
      expect.any(Object)
    );
    expect(actions[0].results?.map((result) => result.logicPath)).toEqual([
      '/done',
      '/next',
    ]);
  });

  it('soft-dismisses an action globally without expecting a JSON body', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);

    await dismissDriftAction('action/2');

    expect(fetchMock).toHaveBeenCalledWith('/api/drift/actions/action%2F2', {
      method: 'DELETE',
      headers: { Accept: 'application/json' },
    });
  });
});
