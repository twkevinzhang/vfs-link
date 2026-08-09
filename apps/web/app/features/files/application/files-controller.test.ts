import { describe, expect, it, vi } from 'vitest';

import type { FilesPort } from './files-port';
import type {
  FileOperationResult,
  FilesResult,
  StatusResult,
  TrashResult,
} from './files-results';
import {
  FILE_PAGE_SIZE,
  FilesController,
  type FilesControllerScheduler,
} from './files-controller';

const status: StatusResult = {
  storageDriver: 'memory',
  storageRoot: '/',
  stats: {
    fileCount: 0,
    directoryCount: 0,
    totalBytes: 0,
    objectCount: 0,
    objectBytes: 0,
  },
  generatedAt: '2026-08-09T00:00:00Z',
};

function files(path: string, query = '', offset = 0): FilesResult {
  return {
    path,
    breadcrumbs: [],
    entries: [],
    pagination: {
      limit: FILE_PAGE_SIZE,
      offset,
      total: 0,
      query,
      hasNext: false,
      hasPrev: offset > 0,
    },
    folderSummary: { files: 0, directories: 0, bytes: 0 },
    visibleBytes: 0,
    generatedAt: '2026-08-09T00:00:00Z',
  };
}

function trash(): TrashResult {
  return { entries: [], generatedAt: '2026-08-09T00:00:00Z' };
}

function operation(statusValue: FileOperationResult['status']) {
  return {
    operationId: 'operation-1',
    type: 'move',
    status: statusValue,
    progress: statusValue === 'completed' ? 1 : 0,
    total: 1,
    createdAt: '2026-08-09T00:00:00Z',
    updatedAt: '2026-08-09T00:00:00Z',
  } satisfies FileOperationResult;
}

function createPort(): FilesPort {
  return {
    getStatus: vi.fn().mockResolvedValue(status),
    getFiles: vi.fn((path, options) =>
      Promise.resolve(files(path, options?.query, options?.offset))
    ),
    getTree: vi.fn(),
    moveFiles: vi.fn().mockResolvedValue({ entries: [] }),
    renameFile: vi.fn().mockResolvedValue({ entries: [] }),
    getFileOperation: vi.fn().mockResolvedValue(operation('completed')),
    moveFilesToTrash: vi.fn().mockResolvedValue({ entries: [] }),
    getTrash: vi.fn().mockResolvedValue(trash()),
    restoreTrash: vi.fn().mockResolvedValue({ entries: [] }),
    deleteTrash: vi.fn().mockResolvedValue({ deleted: 0 }),
    emptyTrash: vi.fn().mockResolvedValue({ deleted: 0 }),
  };
}

function immediateScheduler(): FilesControllerScheduler {
  return {
    schedule(task) {
      task();
      return () => undefined;
    },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

describe('FilesController', () => {
  it('owns initial status and listing request generation', async () => {
    const port = createPort();
    const controller = new FilesController({
      port,
      scheduler: immediateScheduler(),
    });

    controller.enter('docs', 'files');
    await vi.waitFor(() =>
      expect(controller.getSnapshot().loading).toBe(false)
    );

    expect(port.getStatus).toHaveBeenCalledOnce();
    expect(port.getFiles).toHaveBeenCalledWith('docs', {
      query: '',
      limit: FILE_PAGE_SIZE,
      offset: 0,
    });
    expect(controller.getSnapshot().files?.path).toBe('docs');
  });

  it('ignores a stale listing response after a newer search request', async () => {
    const first = deferred<FilesResult>();
    const port = createPort();
    vi.mocked(port.getFiles)
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce(files('docs', 'new'));
    const controller = new FilesController({
      port,
      scheduler: immediateScheduler(),
    });

    controller.enter('docs', 'files');
    controller.setSearchQuery('new');
    await vi.waitFor(() =>
      expect(controller.getSnapshot().files?.pagination.query).toBe('new')
    );
    first.resolve(files('docs', 'stale'));
    await Promise.resolve();

    expect(controller.getSnapshot().files?.pagination.query).toBe('new');
  });

  it('owns trash loading and refreshes after a synchronous mutation', async () => {
    const port = createPort();
    const controller = new FilesController({
      port,
      scheduler: immediateScheduler(),
    });
    controller.enter('', 'trash');
    await vi.waitFor(() =>
      expect(controller.getSnapshot().trashLoading).toBe(false)
    );

    const restored = await controller.restore(['trash-1']);
    await vi.waitFor(() => expect(port.getTrash).toHaveBeenCalledTimes(2));

    expect(restored).toBe(true);
    expect(port.restoreTrash).toHaveBeenCalledWith(['trash-1']);
    expect(port.getStatus).toHaveBeenCalledTimes(2);
  });

  it('monitors asynchronous mutations and clears the terminal operation', async () => {
    const port = createPort();
    vi.mocked(port.moveFiles).mockResolvedValue(operation('pending'));
    const controller = new FilesController({
      port,
      scheduler: immediateScheduler(),
    });
    controller.enter('docs', 'files');
    await vi.waitFor(() =>
      expect(controller.getSnapshot().loading).toBe(false)
    );

    const moved = await controller.move(['docs/a.txt'], 'archive');
    await vi.waitFor(() =>
      expect(controller.getSnapshot().activeOperation).toBeUndefined()
    );

    expect(moved).toBe(true);
    expect(port.getFileOperation).toHaveBeenCalledWith('operation-1');
    expect(port.getFiles).toHaveBeenCalledTimes(2);
  });

  it('debounces completed uploads and refreshes only their active destination', async () => {
    const scheduled: Array<() => void> = [];
    const port = createPort();
    const controller = new FilesController({
      port,
      scheduler: {
        schedule(task) {
          scheduled.push(task);
          return () => undefined;
        },
      },
    });
    controller.enter('docs', 'files');
    await vi.waitFor(() =>
      expect(controller.getSnapshot().loading).toBe(false)
    );

    controller.observeUploads([
      { key: 'upload-1', state: 'complete', destinationPath: 'docs' },
    ]);
    controller.observeUploads([
      { key: 'upload-1', state: 'complete', destinationPath: 'docs' },
    ]);
    expect(scheduled).toHaveLength(1);
    scheduled[0]();
    await vi.waitFor(() => expect(port.getFiles).toHaveBeenCalledTimes(2));

    expect(port.getStatus).toHaveBeenCalledTimes(2);
  });
});
