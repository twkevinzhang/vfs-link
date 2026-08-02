import { describe, expect, it, vi } from 'vitest';

import {
  parseFileViewMode,
  readFileViewMode,
  writeFileViewMode,
} from './file-view-mode';

describe('file view mode', () => {
  it('defaults missing and unsupported values to list', () => {
    expect(parseFileViewMode(null)).toBe('list');
    expect(parseFileViewMode('cards')).toBe('list');
  });

  it('restores the persisted grid preference', () => {
    expect(readFileViewMode({ getItem: () => 'grid' })).toBe('grid');
  });

  it('falls back to list when storage cannot be read', () => {
    expect(
      readFileViewMode({
        getItem: () => {
          throw new Error('storage unavailable');
        },
      })
    ).toBe('list');
  });

  it('persists changes without surfacing storage failures', () => {
    const setItem = vi.fn();
    writeFileViewMode({ setItem }, 'grid');
    expect(setItem).toHaveBeenCalledWith('vfs-link:file-view-mode', 'grid');

    expect(() =>
      writeFileViewMode(
        {
          setItem: () => {
            throw new Error('storage unavailable');
          },
        },
        'list'
      )
    ).not.toThrow();
  });
});
