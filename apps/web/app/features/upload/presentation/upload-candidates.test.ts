import { describe, expect, it, vi } from 'vitest';

import {
  collectDroppedFiles,
  filesToUploadCandidates,
} from './upload-candidates';

describe('session-only upload candidates', () => {
  it('keeps file input metadata without durable browser handles', () => {
    const [candidate] = filesToUploadCandidates([
      new File(['data'], 'fallback.txt'),
    ]);
    expect(candidate).toMatchObject({
      relativePath: 'fallback.txt',
      selectionRoot: 'fallback.txt',
      selectionRootKind: 'file',
    });
    expect(Object.keys(candidate)).toEqual([
      'file',
      'relativePath',
      'selectionRoot',
      'selectionRootKind',
    ]);
  });

  it('walks a dropped directory using the non-durable entry API', async () => {
    const file = new File(['photo'], 'photo.jpg');
    const fileEntry = {
      isFile: true,
      isDirectory: false,
      name: file.name,
      file: (resolve: (value: File) => void) => resolve(file),
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
    const transfer = {
      items: [
        {
          webkitGetAsEntry: vi.fn(() => directoryEntry),
          getAsFile: vi.fn(() => null),
        },
      ],
      files: [],
    } as unknown as DataTransfer;

    await expect(collectDroppedFiles(transfer)).resolves.toMatchObject([
      {
        relativePath: 'photos/photo.jpg',
        selectionRoot: 'photos',
        selectionRootKind: 'directory',
      },
    ]);
  });
});
