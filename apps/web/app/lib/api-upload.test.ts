import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  UploadHttpError,
  cancelUpload,
  createUpload,
  preflightUploads,
} from './api';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('upload API contracts', () => {
  it('maps cancellation to an application-owned void result', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(new Response(null, { status: 204 }));

    await expect(cancelUpload('session-1')).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/uploads/session-1',
      expect.objectContaining({ method: 'DELETE' })
    );
  });

  it('posts a batch preflight using stable client IDs', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [
            {
              clientId: 'queue-1',
              path: 'docs/a.txt',
              status: 'conflict',
              targetVersion: 'opaque-v1',
            },
          ],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      )
    );

    await expect(
      preflightUploads([{ clientId: 'queue-1', path: 'docs/a.txt' }])
    ).resolves.toMatchObject({
      items: [{ clientId: 'queue-1', targetVersion: 'opaque-v1' }],
    });
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/uploads/preflight',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          items: [{ clientId: 'queue-1', path: 'docs/a.txt' }],
        }),
      })
    );
  });

  it('splits preflight requests at the server limit and preserves order', async () => {
    const inputs = Array.from({ length: 1001 }, (_, index) => ({
      clientId: `queue-${index}`,
      path: `docs/${index}.txt`,
    }));
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (_url, init) => {
        const body = JSON.parse(String(init?.body)) as {
          items: typeof inputs;
        };
        return new Response(
          JSON.stringify({
            items: body.items.map((item) => ({
              ...item,
              status: 'available',
              targetVersion: `version-${item.clientId}`,
            })),
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        );
      });

    const response = await preflightUploads(inputs);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(
      JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body)).items
    ).toHaveLength(1000);
    expect(
      JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body)).items
    ).toHaveLength(1);
    expect(response.items.map((item) => item.clientId)).toEqual(
      inputs.map((item) => item.clientId)
    );
  });

  it('sends the preflight target version when overwriting', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ id: 'session-1' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    );

    await createUpload({
      path: 'docs/a.txt',
      size: 3,
      contentType: 'text/plain',
      overwrite: true,
      targetVersion: 'opaque-v1',
    });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/uploads',
      expect.objectContaining({
        body: JSON.stringify({
          path: 'docs/a.txt',
          size: 3,
          contentType: 'text/plain',
          overwrite: true,
          targetVersion: 'opaque-v1',
        }),
      })
    );
  });

  it('preserves the structured race code on upload errors', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          error: 'target changed',
          code: 'UPLOAD_TARGET_CHANGED',
        }),
        {
          status: 409,
          statusText: 'Conflict',
          headers: { 'Content-Type': 'application/json' },
        }
      )
    );

    const error = await createUpload({
      path: 'docs/a.txt',
      size: 3,
      contentType: 'text/plain',
      overwrite: true,
      targetVersion: 'stale',
    }).catch((caught) => caught);

    expect(error).toBeInstanceOf(UploadHttpError);
    expect(error).toMatchObject({
      status: 409,
      code: 'UPLOAD_TARGET_CHANGED',
      message: 'target changed',
    });
  });
});
