export type EntryKind = 'directory' | 'file';

export type FileEntry = {
  path: string;
  name: string;
  kind: EntryKind;
  size: number;
  physicalHash?: string;
  updatedAt: string;
};

export type FilesResponse = {
  path: string;
  breadcrumbs: FileEntry[];
  entries: FileEntry[];
  pagination: Pagination;
  visibleBytes: number;
  stats?: Stats;
  generatedAt: string;
};

export type Pagination = {
  limit: number;
  offset: number;
  total: number;
  query: string;
  hasNext: boolean;
  hasPrev: boolean;
};

export type Stats = {
  fileCount: number;
  directoryCount: number;
  totalBytes: number;
  localObjectCount: number;
  localObjectBytes: number;
};

export type StatusResponse = {
  storageDriver: string;
  storageRoot: string;
  stats: Stats;
  generatedAt: string;
};
