import type { Pagination } from '../../../shared/kernel/pagination';

export type EntryKind = 'directory' | 'file';

export type FolderSummary = {
  files: number;
  directories: number;
  bytes: number;
};

export type FileEntry = {
  path: string;
  name: string;
  kind: EntryKind;
  size: number;
  folderSummary?: FolderSummary;
  physicalHash?: string;
  updatedAt: string;
  trashId?: string;
  trashedAt?: string;
  thumbnail?: { id: string; url: string; width: number; height: number };
};

export type TrashEntry = FileEntry & { trashId: string; trashedAt: string };
export type TrashResponse = { entries: TrashEntry[]; generatedAt: string };
export type FileMutationResponse = { entries: FileEntry[] };
export type DeleteResponse = { deleted: number };

export type FileOperationResponse = {
  operationId: string;
  type: string;
  status: 'pending' | 'running' | 'completed' | 'failed';
  progress: number;
  total: number;
  error?: string;
  entries?: FileEntry[];
  createdAt: string;
  updatedAt: string;
};

export type TreeNode = FileEntry & {
  children?: TreeNode[];
  hasChildren?: boolean;
};

export type FilesResponse = {
  path: string;
  breadcrumbs: FileEntry[];
  entries: FileEntry[];
  pagination: Pagination;
  folderSummary: FolderSummary;
  visibleBytes: number;
  stats?: Stats;
  generatedAt: string;
};

export type Stats = {
  fileCount: number;
  directoryCount: number;
  totalBytes: number;
  objectCount: number;
  objectBytes: number;
};

export type StatusResponse = {
  storageDriver: string;
  storageRoot: string;
  stats: Stats;
  generatedAt: string;
};

export type { Pagination } from '../../../shared/kernel/pagination';
