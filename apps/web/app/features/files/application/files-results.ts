import type { Pagination } from '../../../shared/kernel/pagination';
import type {
  FileEntry,
  FileOperation,
  FolderSummary,
  Stats,
  TrashEntry,
} from '../domain/files';

/** Read models returned by Files application use cases. */
export type FilesResult = {
  path: string;
  breadcrumbs: FileEntry[];
  entries: FileEntry[];
  pagination: Pagination;
  folderSummary: FolderSummary;
  visibleBytes: number;
  stats?: Stats;
  generatedAt: string;
};

export type StatusResult = {
  storageDriver: string;
  storageRoot: string;
  stats: Stats;
  generatedAt: string;
};

export type TrashResult = { entries: TrashEntry[]; generatedAt: string };
export type FileMutationResult = { entries: FileEntry[] };
export type DeleteResult = { deleted: number };
export type FileOperationResult = FileOperation;

export type { Pagination } from '../../../shared/kernel/pagination';
