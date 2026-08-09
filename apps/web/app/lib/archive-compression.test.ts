import { BlobReader, BlobWriter, ZipReader } from '@zip.js/zip.js';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { buildArchives } from './archive-compression';
import {
  createArchiveTemporaryManifest,
  createArchiveTemporaryName,
  findArchiveTemporaryOrphanNames,
  getArchiveTemporaryOwnerId,
  isArchiveTemporaryManifest,
  listArchiveTemporaryStorageUsage,
  removeArchiveTemporaryFiles,
} from './archive-temporary-storage';
import type { ArchivePlan } from './archive-plan';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('buildArchives', () => {
  it('creates a readable AES-256 Zip64 archive without exposing the password', async () => {
    const source = new File(['classified'], 'secret.txt', {
      type: 'text/plain',
      lastModified: 1,
    });
    const plan: ArchivePlan = {
      id: 'secure',
      name: 'secure.zip',
      entries: [{ file: source, path: source.name }],
      thumbnailCandidates: [],
    };
    const [built] = await buildArchives([plan], {
      compressionLevel: 6,
      splitSize: 0,
      password: 'correct horse battery staple',
    });
    expect(built.files.map((item) => item.relativePath)).toEqual([
      'secure.zip',
    ]);

    const reader = new ZipReader(new BlobReader(built.files[0].file));
    const [entry] = await reader.getEntries();
    expect(entry.encrypted).toBe(true);
    expect(entry.zipCrypto).toBe(false);
    expect(entry.extraFieldAES?.strength).toBe(3);
    if (!('getData' in entry)) throw new Error('expected a file entry');
    const output = await entry.getData(new BlobWriter(), {
      password: 'correct horse battery staple',
    });
    expect(await output.text()).toBe('classified');
    await reader.close();
  });

  it('emits standard split-volume names with the final central directory in .zip', async () => {
    const source = new File(['x'.repeat(4_000)], 'large.txt');
    const [built] = await buildArchives(
      [
        {
          id: 'split',
          name: 'split.zip',
          entries: [{ file: source, path: source.name }],
          thumbnailCandidates: [],
        },
      ],
      { compressionLevel: 0, splitSize: 512, password: '' }
    );
    expect(built.files.length).toBeGreaterThan(1);
    expect(built.files[0].relativePath).toBe('split.z01');
    expect(built.files.at(-1)?.relativePath).toBe('split.zip');
  });
});

describe('archive temporary storage', () => {
  it('creates a JSON-safe manifest with names tied to one owner', () => {
    const name = createArchiveTemporaryName('archive-owner', 'part-1');
    const manifest = createArchiveTemporaryManifest(
      'archive-owner',
      [{ name, size: 123 }],
      456
    );

    expect(getArchiveTemporaryOwnerId(name)).toBe('archive-owner');
    expect(isArchiveTemporaryManifest(manifest)).toBe(true);
    expect(JSON.parse(JSON.stringify(manifest))).toEqual(manifest);
    expect(Object.values(manifest.files[0])).not.toContainEqual(
      expect.any(Blob)
    );
  });

  it('rejects manifests that claim files owned by another archive', () => {
    const name = createArchiveTemporaryName('another-owner', 'part-1');

    expect(() =>
      createArchiveTemporaryManifest('archive-owner', [{ name, size: 1 }])
    ).toThrow('Archive temporary file metadata is invalid');
    expect(
      isArchiveTemporaryManifest({
        version: 1,
        ownerId: 'archive-owner',
        createdAt: 1,
        files: [{ name, size: 1 }],
      })
    ).toBe(false);
  });

  it('returns an ownership manifest for archives written to OPFS', async () => {
    const handles = new Map<
      string,
      {
        kind: 'file';
        name: string;
        createWritable: () => Promise<WritableStream<Uint8Array>>;
        getFile: () => Promise<File>;
      }
    >();
    const directory = {
      kind: 'directory',
      name: '_vfs-link-archive-output',
      getFileHandle: vi.fn(async (name: string) => {
        const chunks: BlobPart[] = [];
        const handle = {
          kind: 'file' as const,
          name,
          createWritable: async () =>
            new WritableStream<Uint8Array>({
              write: (chunk) => {
                chunks.push(chunk);
              },
            }),
          getFile: async () => new File(chunks, name),
        };
        handles.set(name, handle);
        return handle;
      }),
      removeEntry: vi.fn(),
    };
    vi.stubGlobal('navigator', {
      storage: {
        getDirectory: vi.fn(async () => ({
          getDirectoryHandle: vi.fn(async () => directory),
        })),
      },
    });
    const source = new File(['archive source'], 'source.txt');

    const [built] = await buildArchives(
      [
        {
          id: 'opfs',
          name: 'opfs.zip',
          entries: [{ file: source, path: source.name }],
          thumbnailCandidates: [],
        },
      ],
      { compressionLevel: 0, splitSize: 0, password: '' }
    );

    expect(built.temporaryNames).toHaveLength(1);
    expect(built.temporaryManifest).toEqual({
      version: 1,
      ownerId: built.id,
      createdAt: expect.any(Number),
      files: [
        {
          name: built.temporaryNames[0],
          size: built.files[0].file.size,
        },
      ],
    });
    expect(getArchiveTemporaryOwnerId(built.temporaryNames[0])).toBe(built.id);
    expect(handles.get(built.temporaryNames[0])).toBe(
      built.files[0].fileHandle
    );
  });

  it('lists OPFS usage and removes only the requested names', async () => {
    const firstName = createArchiveTemporaryName('owner-1', 'part-1');
    const secondName = createArchiveTemporaryName('owner-2', 'part-1');
    const files = new Map([
      [firstName, new File(['abc'], firstName, { lastModified: 10 })],
      [secondName, new File(['12345'], secondName, { lastModified: 20 })],
    ]);
    const removeEntry = vi.fn(async (name: string) => {
      files.delete(name);
    });
    const directory = {
      kind: 'directory',
      name: '_vfs-link-archive-output',
      async *entries() {
        for (const [name, file] of files) {
          yield [
            name,
            {
              kind: 'file',
              name,
              getFile: async () => file,
            },
          ];
        }
      },
      removeEntry,
    };
    const getDirectoryHandle = vi.fn(async () => directory);
    vi.stubGlobal('navigator', {
      storage: {
        getDirectory: vi.fn(async () => ({ getDirectoryHandle })),
      },
    });

    await expect(listArchiveTemporaryStorageUsage()).resolves.toEqual({
      files: [
        {
          name: firstName,
          size: 3,
          lastModified: 10,
          ownerId: 'owner-1',
        },
        {
          name: secondName,
          size: 5,
          lastModified: 20,
          ownerId: 'owner-2',
        },
      ],
      fileCount: 2,
      totalBytes: 8,
    });

    await removeArchiveTemporaryFiles([firstName, firstName]);

    expect(removeEntry).toHaveBeenCalledTimes(1);
    expect(removeEntry).toHaveBeenCalledWith(firstName);
    expect(files.has(firstName)).toBe(false);
    expect(files.has(secondName)).toBe(true);
  });

  it('finds only old files that are absent from valid retained manifests', () => {
    const retained = createArchiveTemporaryName('active', 'part-1');
    const recent = createArchiveTemporaryName('orphan', 'recent');
    const old = createArchiveTemporaryName('orphan', 'old');
    const manifest = createArchiveTemporaryManifest(
      'active',
      [{ name: retained, size: 1 }],
      1
    );

    expect(
      findArchiveTemporaryOrphanNames(
        [
          { name: retained, size: 1, lastModified: 10, ownerId: 'active' },
          { name: recent, size: 1, lastModified: 101, ownerId: 'orphan' },
          { name: old, size: 1, lastModified: 99, ownerId: 'orphan' },
          { name: 'legacy-file', size: 1, lastModified: 50 },
        ],
        [manifest, { malformed: true }],
        100
      )
    ).toEqual([old, 'legacy-file']);
  });

  it('does not create the OPFS directory while listing or clearing an absent one', async () => {
    const notFound = Object.assign(new Error('missing'), {
      name: 'NotFoundError',
    });
    const getDirectoryHandle = vi.fn(async () => {
      throw notFound;
    });
    vi.stubGlobal('navigator', {
      storage: {
        getDirectory: vi.fn(async () => ({ getDirectoryHandle })),
      },
    });

    await expect(listArchiveTemporaryStorageUsage()).resolves.toEqual({
      files: [],
      fileCount: 0,
      totalBytes: 0,
    });
    await expect(
      removeArchiveTemporaryFiles(['missing-file'])
    ).resolves.toBeUndefined();
    expect(getDirectoryHandle).toHaveBeenCalledTimes(2);
    expect(getDirectoryHandle).toHaveBeenNthCalledWith(
      1,
      '_vfs-link-archive-output',
      { create: false }
    );
    expect(getDirectoryHandle).toHaveBeenNthCalledWith(
      2,
      '_vfs-link-archive-output',
      { create: false }
    );
  });
});
