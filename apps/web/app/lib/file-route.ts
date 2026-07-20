import { normalizePath } from './format';

export const FILES_ROUTE = '/files';
export const TRASH_ROUTE = '/trash';

export function fileBrowserPath(logicalPath: string) {
  const normalizedPath = normalizePath(logicalPath);
  if (normalizedPath === '/') {
    return FILES_ROUTE;
  }

  const encodedPath = normalizedPath
    .slice(1)
    .split('/')
    .map((segment) => encodeURIComponent(segment))
    .join('/');
  return `${FILES_ROUTE}/${encodedPath}`;
}

export function logicalPathFromRoute(routePath: string | undefined) {
  return normalizePath(routePath ? `/${routePath}` : '/');
}
