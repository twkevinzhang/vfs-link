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
export type FileOperation = {
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

export type Stats = {
  fileCount: number;
  directoryCount: number;
  totalBytes: number;
  objectCount: number;
  objectBytes: number;
};
