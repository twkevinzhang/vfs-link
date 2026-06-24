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
  stats: Stats;
  generatedAt: string;
};

export type TreeNode = {
  path: string;
  name: string;
  kind: EntryKind;
  children?: TreeNode[];
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
