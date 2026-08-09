// An empty base keeps browser requests on the same origin as the Web UI.
// Set VITE_API_BASE_URL only for an intentionally separate API deployment.
export const API_BASE_URL = (import.meta.env.VITE_API_BASE_URL || '').replace(
  /\/$/,
  ''
);

export type RequestOptions = { signal?: AbortSignal };

export class HttpError extends Error {
  constructor(
    message: string,
    public readonly status?: number,
    public readonly code?: string
  ) {
    super(message);
    this.name = 'HttpError';
  }
}

async function errorDetails(response: Response) {
  const fallback = `${response.status} ${response.statusText}`;
  try {
    const body = (await response.json()) as {
      error?: string;
      code?: string;
    };
    return {
      message: body.error || fallback,
      code: body.code,
    };
  } catch {
    return { message: fallback, code: undefined };
  }
}

export async function requestJson<T>(
  path: string,
  options: RequestOptions = {}
): Promise<T> {
  const response = await fetch(apiUrl(path), {
    headers: { Accept: 'application/json' },
    signal: options.signal,
  });
  if (!response.ok) {
    const details = await errorDetails(response);
    throw new HttpError(details.message, response.status, details.code);
  }
  return response.json() as Promise<T>;
}

export async function postJson<T>(
  path: string,
  body: unknown,
  options: RequestOptions = {}
): Promise<T> {
  const response = await fetch(apiUrl(path), {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(body),
    signal: options.signal,
  });
  if (!response.ok) {
    const details = await errorDetails(response);
    throw new HttpError(details.message, response.status, details.code);
  }
  return response.json() as Promise<T>;
}

export async function deleteResource(
  path: string,
  options: RequestOptions = {}
): Promise<void> {
  const response = await fetch(apiUrl(path), {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
    signal: options.signal,
  });
  if (!response.ok) {
    const details = await errorDetails(response);
    throw new HttpError(details.message, response.status, details.code);
  }
}

export function apiUrl(pathOrUrl: string) {
  if (/^https?:\/\//i.test(pathOrUrl)) return pathOrUrl;
  return `${API_BASE_URL}${pathOrUrl.startsWith('/') ? '' : '/'}${pathOrUrl}`;
}
