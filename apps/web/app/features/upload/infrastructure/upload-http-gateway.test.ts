import { afterEach, describe, expect, it, vi } from 'vitest';

import { BrowserUploadSourceRegistry } from './browser-upload-source-registry';

const { postJson } = vi.hoisted(() => ({ postJson: vi.fn() }));
vi.mock('../../../shared/infrastructure/http/http-client', () => ({
  HttpError: class HttpError extends Error {
    constructor(message: string, readonly status?: number) {
      super(message);
    }
  },
  apiUrl: (value: string) => value,
  deleteResource: vi.fn(async () => undefined),
  postJson,
}));

const dto = {
  id: 'session-1',
  logicPath: 'hello.txt',
  size: 5,
  contentType: 'text/plain',
  status: 'uploading' as const,
  uploadedSize: 0,
  method: 'PUT' as const,
  uploadUrl: '/private-upload-url',
  headers: { Authorization: 'private-header' },
  completeUrl: '/private-complete-url',
  statusUrl: '/private-status-url',
  expiresAt: '2099-01-01T00:00:00Z',
};

afterEach(() => {
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe('upload HTTP transport boundary', () => {
  it('keeps URLs, headers, and HTTP method out of the application session', async () => {
    postJson.mockResolvedValueOnce(dto);
    const { createUploadHttpGateway } = await import('./upload-http-gateway');
    const gateway = createUploadHttpGateway(new BrowserUploadSourceRegistry());

    const session = await gateway.createUpload({
      path: 'hello.txt',
      size: 5,
      contentType: 'text/plain',
      overwrite: false,
    });

    expect(session).toEqual({
      id: 'session-1',
      status: 'uploading',
      uploadedSize: 0,
      error: undefined,
      expiresAt: '2099-01-01T00:00:00Z',
    });
    expect(session).not.toHaveProperty('uploadUrl');
    expect(session).not.toHaveProperty('headers');
    expect(session).not.toHaveProperty('method');

    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ ...dto, uploadedSize: 3 }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
      )
    );
    await expect(gateway.getUploadSession(session)).resolves.toMatchObject({
      id: 'session-1',
      uploadedSize: 3,
    });
    expect(fetch).toHaveBeenCalledWith(
      '/private-status-url',
      expect.any(Object)
    );
  });
});
