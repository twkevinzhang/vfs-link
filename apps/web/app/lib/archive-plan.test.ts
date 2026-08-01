import { describe, expect, it } from 'vitest';

import { buildArchivePlans, splitArchiveNames } from './archive-plan';
import type { UploadCandidate } from './folder-upload';

function candidate(
  relativePath: string,
  selectionRoot: string,
  selectionRootKind: UploadCandidate['selectionRootKind']
): UploadCandidate {
  const name = relativePath.split('/').at(-1) ?? relativePath;
  return {
    file: new File(['x'], name, {
      type: /\.(jpg)$/i.test(name) ? 'image/jpeg' : '',
    }),
    relativePath,
    selectionRoot,
    selectionRootKind,
  };
}

const fixture = [
  candidate('readme.txt', 'readme.txt', 'file'),
  candidate('cover.jpg', 'cover.jpg', 'file'),
  candidate('photos/a.jpg', 'photos', 'directory'),
  candidate('photos/b.jpg', 'photos', 'directory'),
  candidate('docs/manual.pdf', 'docs', 'directory'),
];

const base = {
  archiveName: 'upload.zip',
  compressionLevel: 6,
  splitSize: 0,
  password: '',
  oneArchivePerItem: false,
  preserveExtension: false,
  recurseFolders: false,
};

describe('buildArchivePlans', () => {
  it('places every selected item in one named archive by default', () => {
    const plans = buildArchivePlans(fixture, base);
    expect(plans.map((plan) => plan.name)).toEqual(['upload.zip']);
    expect(plans[0].entries.map((entry) => entry.path)).toEqual([
      'cover.jpg',
      'docs/manual.pdf',
      'photos/a.jpg',
      'photos/b.jpg',
      'readme.txt',
    ]);
  });

  it('treats each selected folder as one item when recursion is off', () => {
    const plans = buildArchivePlans(fixture, {
      ...base,
      oneArchivePerItem: true,
    });
    expect(plans.map((plan) => plan.name)).toEqual([
      'cover.zip',
      'docs.zip',
      'photos.zip',
      'readme.zip',
    ]);
  });

  it('recurses into selected folders and creates one archive per file', () => {
    const plans = buildArchivePlans(fixture, {
      ...base,
      oneArchivePerItem: true,
      recurseFolders: true,
    });
    expect(plans.map((plan) => plan.name)).toEqual([
      'cover.zip',
      'manual.zip',
      'a.zip',
      'b.zip',
      'readme.zip',
    ]);
  });

  it('keeps double extensions and avoids case-insensitive collisions', () => {
    const plans = buildArchivePlans(
      [
        candidate('report.docx', 'report.docx', 'file'),
        candidate('report.pdf', 'report.pdf', 'file'),
      ],
      { ...base, oneArchivePerItem: true }
    );
    expect(plans.map((plan) => plan.name)).toEqual([
      'report.zip',
      'report (2).zip',
    ]);
    expect(
      buildArchivePlans([fixture[0]], {
        ...base,
        oneArchivePerItem: true,
        preserveExtension: true,
      })[0].name
    ).toBe('readme.txt.zip');
  });
});

it('uses standard ZIP split-volume names', () => {
  expect(splitArchiveNames('photos.zip', 3)).toEqual([
    'photos.z01',
    'photos.z02',
    'photos.zip',
  ]);
});
