const byteUnits = ['B', 'KB', 'MB', 'GB', 'TB'];
export const ROOT_LABEL = 'root';

export function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) {
    return '0 B';
  }

  const exponent = Math.min(
    Math.floor(Math.log(value) / Math.log(1024)),
    byteUnits.length - 1
  );
  const size = value / 1024 ** exponent;
  const digits = size >= 100 || exponent === 0 ? 0 : 1;
  return `${size.toFixed(digits)} ${byteUnits[exponent]}`;
}

export function formatDate(value: string) {
  if (!value) {
    return '-';
  }

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return '-';
  }

  return new Intl.DateTimeFormat('zh-TW', {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(date);
}

export function normalizePath(value: string) {
  if (!value || value === '.') return '';
  return value
    .normalize('NFC')
    .replace(/\/+/g, '/')
    .replace(/^\/+|\/+$/g, '');
}

export function formatPathDisplayName(path: string, name?: string) {
  const normalizedPath = normalizePath(path);
  if (normalizedPath === '') {
    return ROOT_LABEL;
  }

  if (name) {
    return name;
  }

  const parts = normalizedPath.split('/').filter(Boolean);
  return parts[parts.length - 1] ?? ROOT_LABEL;
}
