import { BlobReader, BlobWriter, ZipReader } from '@zip.js/zip.js';
import { describe, expect, it } from 'vitest';

import { buildArchives } from './archive-compression';
import type { ArchivePlan } from './archive-plan';

describe('session-only archive compression', () => {
  it('creates a readable in-memory AES-256 Zip64 archive', async () => {
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

  it('emits split volumes entirely in memory', async () => {
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
