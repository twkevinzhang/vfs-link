import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  chooseDirectoryWithHandles,
  chooseFilesWithHandles,
  collectDroppedFiles,
  filesToUploadCandidates,
} from './folder-upload';

function fileHandle(name: string, content = name) {
  const file = new File([content], name);
  return {
    kind: 'file' as const,
    name,
    getFile: vi.fn(async () => file),
  } as unknown as FileSystemFileHandle;
}

function directoryHandle(name: string, children: FileSystemHandle[]) {
  return {
    kind: 'directory' as const,
    name,
    async *values() {
      yield* children;
    },
  } as unknown as FileSystemDirectoryHandle;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('upload source handle persistence', () => {
  it('marks file-input fallback candidates as non-durable', () => {
    const candidates = filesToUploadCandidates([
      new File(['data'], 'fallback.txt'),
    ]);

    expect(candidates).toHaveLength(1);
    expect(candidates[0].sourceHandlePersistence).toBe('non-durable');
    expect(candidates[0].fileHandle).toBeUndefined();
  });

  it('marks file-picker handles as durable', async () => {
    const handle = fileHandle('durable.txt');
    vi.stubGlobal('window', {
      showOpenFilePicker: vi.fn(async () => [handle]),
    });

    const candidates = await chooseFilesWithHandles();

    expect(candidates?.[0].sourceHandlePersistence).toBe('durable');
    expect(candidates?.[0].fileHandle).toBe(handle);
  });

  it('marks every file reached through a directory handle as durable', async () => {
    const nested = directoryHandle('photos', [
      fileHandle('cover.jpg'),
      directoryHandle('originals', [fileHandle('source.raw')]),
    ]);
    vi.stubGlobal('window', {
      showDirectoryPicker: vi.fn(async () => nested),
    });

    const candidates = await chooseDirectoryWithHandles();

    expect(candidates?.map((candidate) => candidate.relativePath)).toEqual([
      'photos/cover.jpg',
      'photos/originals/source.raw',
    ]);
    expect(
      candidates?.every(
        (candidate) => candidate.sourceHandlePersistence === 'durable'
      )
    ).toBe(true);
  });

  it('keeps durable and fallback files in a mixed drag-drop operation', async () => {
    const durable = fileHandle('durable.txt');
    const fallback = new File(['fallback'], 'fallback.txt');
    const dataTransfer = {
      items: [
        {
          getAsFileSystemHandle: vi.fn(async () => durable),
          getAsFile: vi.fn(() => null),
        },
        {
          getAsFileSystemHandle: vi.fn(async () => null),
          getAsFile: vi.fn(() => fallback),
        },
      ],
      files: [],
    } as unknown as DataTransfer;

    const candidates = await collectDroppedFiles(dataTransfer);

    expect(
      candidates.map((candidate) => [
        candidate.relativePath,
        candidate.sourceHandlePersistence,
      ])
    ).toEqual([
      ['durable.txt', 'durable'],
      ['fallback.txt', 'non-durable'],
    ]);
  });

  it('keeps legacy drag-drop folders as non-durable fallback sources', async () => {
    const fallback = new File(['photo'], 'photo.jpg');
    const fileEntry = {
      isFile: true,
      isDirectory: false,
      name: fallback.name,
      file: (resolve: (file: File) => void) => resolve(fallback),
    } as unknown as FileSystemFileEntry;
    let readCount = 0;
    const directoryEntry = {
      isFile: false,
      isDirectory: true,
      name: 'photos',
      createReader: () => ({
        readEntries: (resolve: (entries: FileSystemEntry[]) => void) => {
          resolve(readCount++ === 0 ? [fileEntry] : []);
        },
      }),
    } as unknown as FileSystemDirectoryEntry;
    const dataTransfer = {
      items: [
        {
          webkitGetAsEntry: vi.fn(() => directoryEntry),
          getAsFile: vi.fn(() => null),
        },
      ],
      files: [],
    } as unknown as DataTransfer;

    const candidates = await collectDroppedFiles(dataTransfer);

    expect(candidates).toHaveLength(1);
    expect(candidates[0]).toMatchObject({
      relativePath: 'photos/photo.jpg',
      selectionRoot: 'photos',
      selectionRootKind: 'directory',
      sourceHandlePersistence: 'non-durable',
    });
  });
});
