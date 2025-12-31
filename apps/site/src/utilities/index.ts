export function latestIndex<T>(array: Array<T>): number {
  return array.length - 1;
}

export function findOrThrow<T>(array: Array<T>, f: (arg: T) => {}): T {
  const r = array.find(f);
  if (!r) {
    throw new Error('not found');
  }
  return r;
}

export function joinPath(...paths: string[]): string {
  return paths.join('/').replace(/\/+/g, '/');
}

export function count(a: string, target: string): number {
  return a.split(target).length - 1;
}

export function removeLeadingSlash(path: string): string {
  return path.startsWith('/') ? path.slice(1) : path;
}

export function removeTrailingSlash(path: string): string {
  return path.endsWith('/') ? path.slice(0, -1) : path;
}

export function removeTrailingPart(path: string): string {
  path = removeTrailingSlash(path);
  const lastSlashIndex = path.lastIndexOf('/');
  if (lastSlashIndex === -1) return '';
  return path.slice(0, lastSlashIndex);
}

export function getFilename(path: string): string {
  if (count(path, '/') === 0) {
    return path;
  }
  return path.split('/').pop() || '';
}

export function decodeBuffer(data: any): string {
  if (typeof data === 'string') return data;
  if (data?.type === 'Buffer' && Array.isArray(data.data)) {
    return new TextDecoder().decode(new Uint8Array(data.data));
  }
  return data?.toString() || '';
}

export function isNotEmpty(obj: any): boolean {
  return !isEmpty(obj);
}

export * from './hash';
export * from './download';
