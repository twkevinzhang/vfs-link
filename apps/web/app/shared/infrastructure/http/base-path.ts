const ABSOLUTE_URL_PATTERN = /^[a-z][a-z\d+\-.]*:/i;

export const APP_BASE_PATH = normalizeBasePath(import.meta.env.BASE_URL);

export function normalizeBasePath(value: string | null | undefined) {
  const trimmed = value?.trim();
  if (!trimmed || trimmed === '/') {
    return '/';
  }

  const withoutSlashes = trimmed.replace(/^\/+|\/+$/g, '');
  return withoutSlashes ? `/${withoutSlashes}` : '/';
}

export function appPath(path: string) {
  if (!path) {
    return APP_BASE_PATH;
  }
  if (ABSOLUTE_URL_PATTERN.test(path) || path.startsWith('#')) {
    return path;
  }
  if (path.startsWith('?')) {
    return APP_BASE_PATH === '/' ? `/${path}` : `${APP_BASE_PATH}${path}`;
  }

  const normalizedPath = path.startsWith('/') ? path : `/${path}`;
  if (APP_BASE_PATH === '/') {
    return normalizedPath;
  }
  if (normalizedPath === APP_BASE_PATH) {
    return normalizedPath;
  }
  if (normalizedPath.startsWith(`${APP_BASE_PATH}/`)) {
    return normalizedPath;
  }
  if (normalizedPath === '/') {
    return APP_BASE_PATH;
  }
  return `${APP_BASE_PATH}${normalizedPath}`;
}
