import type { TrashEntry } from '../domain/files';
import type { FilesPort } from './files-port';
import type {
  DeleteResult,
  FileMutationResult,
  FileOperationResult,
  FilesResult,
  StatusResult,
} from './files-results';
import { watchFileOperation } from './watch-file-operation';

export const FILE_PAGE_SIZE = 100;
const UPLOAD_REFRESH_DEBOUNCE_MS = 300;

export type FileBrowserView = 'files' | 'trash';

export type FilesControllerState = {
  currentPath: string;
  view: FileBrowserView;
  searchQuery: string;
  pageOffset: number;
  status?: StatusResult;
  files?: FilesResult;
  loading: boolean;
  error?: string;
  trashEntries: TrashEntry[];
  trashLoading: boolean;
  actionError?: string;
  activeOperation?: FileOperationResult;
};

export type FilesControllerScheduler = {
  schedule(task: () => void, delayMs: number): () => void;
};

export type ObservedUploadItem = {
  key: string;
  state: string;
  destinationPath: string;
};

export type FilesControllerDependencies = {
  port: FilesPort;
  scheduler: FilesControllerScheduler;
};

type Listener = () => void;

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}

export class FilesController {
  private state: FilesControllerState = {
    currentPath: '',
    view: 'files',
    searchQuery: '',
    pageOffset: 0,
    loading: true,
    trashEntries: [],
    trashLoading: false,
  };
  private readonly listeners = new Set<Listener>();
  private filesRequestGeneration = 0;
  private routeGeneration = 0;
  private operationCancellation?: { cancelled: boolean };
  private cancelUploadRefresh?: () => void;
  private readonly completedUploadKeys = new Set<string>();
  private readonly completedUploadDestinations = new Set<string>();

  constructor(private readonly dependencies: FilesControllerDependencies) {}

  readonly subscribe = (listener: Listener) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  readonly getSnapshot = () => this.state;

  enter(currentPath: string, view: FileBrowserView) {
    const routeChanged =
      currentPath !== this.state.currentPath || view !== this.state.view;
    if (routeChanged) {
      this.routeGeneration += 1;
      this.filesRequestGeneration += 1;
      this.update({
        currentPath,
        view,
        searchQuery: '',
        pageOffset: 0,
        files: view === 'files' ? this.state.files : undefined,
      });
    }
    void this.loadStatus();
    if (view === 'trash') {
      void this.loadTrash();
    } else {
      void this.loadFiles(currentPath, '', 0);
    }
  }

  leave() {
    this.routeGeneration += 1;
    this.filesRequestGeneration += 1;
    this.cancelOperation();
    this.cancelUploadRefresh?.();
    this.cancelUploadRefresh = undefined;
    this.completedUploadDestinations.clear();
  }

  setSearchQuery(searchQuery: string) {
    const normalizedQuery = searchQuery.trim();
    if (
      normalizedQuery === this.state.searchQuery &&
      this.state.pageOffset === 0
    ) {
      return;
    }
    this.update({ searchQuery: normalizedQuery, pageOffset: 0 });
    if (this.state.view === 'files') {
      void this.loadFiles(this.state.currentPath, normalizedQuery, 0);
    }
  }

  setPageOffset(pageOffset: number) {
    if (pageOffset === this.state.pageOffset) return;
    this.update({ pageOffset });
    if (this.state.view === 'files') {
      void this.loadFiles(
        this.state.currentPath,
        this.state.searchQuery,
        pageOffset
      );
    }
  }

  refresh() {
    this.update({ pageOffset: 0 });
    void this.loadStatus();
    if (this.state.view === 'trash') {
      void this.loadTrash();
    } else {
      void this.loadFiles(this.state.currentPath, this.state.searchQuery, 0);
    }
  }

  async loadStatus() {
    const routeGeneration = this.routeGeneration;
    try {
      const status = await this.dependencies.port.getStatus();
      if (routeGeneration === this.routeGeneration) this.update({ status });
    } catch (error) {
      if (routeGeneration === this.routeGeneration) {
        this.update({
          error: errorMessage(error, 'Unable to load status'),
        });
      }
    }
  }

  async loadTrash() {
    const routeGeneration = this.routeGeneration;
    this.update({ trashLoading: true, actionError: undefined });
    try {
      const response = await this.dependencies.port.getTrash();
      if (routeGeneration === this.routeGeneration) {
        this.update({ trashEntries: response.entries, trashLoading: false });
      }
    } catch (error) {
      if (routeGeneration === this.routeGeneration) {
        this.update({
          trashLoading: false,
          actionError: errorMessage(error, 'Unable to load trash'),
        });
      }
    }
  }

  async rename(path: string, name: string) {
    try {
      const result = await this.dependencies.port.renameFile(path, name);
      this.acceptMutation(result);
    } catch (error) {
      this.failAction(error, 'Unable to rename item');
      throw error instanceof Error ? error : new Error('Unable to rename item');
    }
  }

  async move(paths: string[], destination: string) {
    try {
      this.acceptMutation(
        await this.dependencies.port.moveFiles(paths, destination)
      );
      return true;
    } catch (error) {
      this.failAction(error, 'Unable to move items');
      return false;
    }
  }

  async moveToTrash(paths: string[]) {
    try {
      this.acceptMutation(await this.dependencies.port.moveFilesToTrash(paths));
      return true;
    } catch (error) {
      this.failAction(error, 'Unable to move items to trash');
      return false;
    }
  }

  async restore(trashIds: string[]) {
    try {
      this.acceptMutation(await this.dependencies.port.restoreTrash(trashIds));
      return true;
    } catch (error) {
      this.failAction(error, 'Unable to restore items');
      return false;
    }
  }

  async deletePermanently(trashIds: string[]) {
    try {
      this.acceptMutation(await this.dependencies.port.deleteTrash(trashIds));
      return true;
    } catch (error) {
      this.failAction(error, 'Unable to delete items');
      return false;
    }
  }

  async emptyTrash() {
    try {
      this.acceptMutation(await this.dependencies.port.emptyTrash());
      return true;
    } catch (error) {
      this.failAction(error, 'Unable to empty trash');
      return false;
    }
  }

  observeUploads(items: ObservedUploadItem[]) {
    const activeKeys = new Set(items.map((item) => item.key));
    for (const key of this.completedUploadKeys) {
      if (!activeKeys.has(key)) this.completedUploadKeys.delete(key);
    }
    let foundCompletion = false;
    for (const item of items) {
      if (item.state !== 'complete' || this.completedUploadKeys.has(item.key)) {
        continue;
      }
      this.completedUploadKeys.add(item.key);
      this.completedUploadDestinations.add(item.destinationPath);
      foundCompletion = true;
    }
    if (!foundCompletion) return;

    this.cancelUploadRefresh?.();
    this.cancelUploadRefresh = this.dependencies.scheduler.schedule(() => {
      this.cancelUploadRefresh = undefined;
      const destinations = new Set(this.completedUploadDestinations);
      this.completedUploadDestinations.clear();
      void this.loadStatus();
      if (
        this.state.view === 'files' &&
        destinations.has(this.state.currentPath)
      ) {
        this.update({ pageOffset: 0 });
        void this.loadFiles(this.state.currentPath, this.state.searchQuery, 0);
      }
    }, UPLOAD_REFRESH_DEBOUNCE_MS);
  }

  private async loadFiles(path: string, searchQuery: string, offset: number) {
    const requestGeneration = ++this.filesRequestGeneration;
    this.update({ loading: true, error: undefined });
    try {
      const files = await this.dependencies.port.getFiles(path, {
        query: searchQuery,
        limit: FILE_PAGE_SIZE,
        offset,
      });
      if (requestGeneration === this.filesRequestGeneration) {
        this.update({ files, loading: false });
      }
    } catch (error) {
      if (requestGeneration === this.filesRequestGeneration) {
        this.update({
          loading: false,
          error: errorMessage(error, 'Unable to load files'),
        });
      }
    }
  }

  private acceptMutation(
    result: FileMutationResult | DeleteResult | FileOperationResult
  ) {
    this.update({ actionError: undefined });
    if ('operationId' in result) {
      this.update({ activeOperation: result });
      void this.monitorOperation(result.operationId);
      return;
    }
    this.refreshAfterMutation();
  }

  private async monitorOperation(operationId: string) {
    this.cancelOperation();
    const cancellation = { cancelled: false };
    this.operationCancellation = cancellation;
    try {
      const operation = await watchFileOperation({
        id: operationId,
        cancellation,
        fetchOperation: (id) => this.dependencies.port.getFileOperation(id),
        onUpdate: (activeOperation) => {
          if (this.operationCancellation === cancellation) {
            this.update({ activeOperation });
          }
        },
      });
      if (this.operationCancellation !== cancellation) return;
      this.update({ activeOperation: undefined });
      if (operation.status === 'completed') {
        this.refreshAfterMutation();
      } else {
        this.update({
          actionError: operation.error || 'Background operation failed',
        });
      }
    } catch (error) {
      if (
        cancellation.cancelled ||
        this.operationCancellation !== cancellation
      ) {
        return;
      }
      this.update({
        activeOperation: undefined,
        actionError: errorMessage(
          error,
          'Unable to monitor background operation'
        ),
      });
    } finally {
      if (this.operationCancellation === cancellation) {
        this.operationCancellation = undefined;
      }
    }
  }

  private refreshAfterMutation() {
    this.update({ pageOffset: 0 });
    void this.loadStatus();
    if (this.state.view === 'trash') void this.loadTrash();
    else void this.loadFiles(this.state.currentPath, this.state.searchQuery, 0);
  }

  private cancelOperation() {
    if (this.operationCancellation) this.operationCancellation.cancelled = true;
    this.operationCancellation = undefined;
  }

  private failAction(error: unknown, fallback: string) {
    this.update({ actionError: errorMessage(error, fallback) });
  }

  private update(patch: Partial<FilesControllerState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
}
