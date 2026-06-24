const DEFAULT_BASE_PATH = '/';

export function normalizeBasePath(value: string | null | undefined) {
  const trimmed = value?.trim();
  if (!trimmed || trimmed === DEFAULT_BASE_PATH) {
    return DEFAULT_BASE_PATH;
  }

  const pathname = parsePathname(trimmed);
  const withoutSlashes = pathname.replace(/^\/+|\/+$/g, '');
  return withoutSlashes ? `/${withoutSlashes}` : DEFAULT_BASE_PATH;
}

export function viteBase(basePath: string) {
  return basePath === DEFAULT_BASE_PATH ? basePath : `${basePath}/`;
}

function parsePathname(value: string) {
  try {
    if (/^https?:\/\//i.test(value)) {
      return new URL(value).pathname;
    }
  } catch {
    return value;
  }

  return value;
}
