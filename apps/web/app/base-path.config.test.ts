import { describe, expect, it } from 'vitest';

import { normalizeBasePath, viteBase } from '../base-path.config';

describe('base path build configuration', () => {
  it('uses the normalized path as both the router basename and raw Vite base', () => {
    const basePath = normalizeBasePath('/vfs-link/viewer/');

    expect(basePath).toBe('/vfs-link/viewer');
    expect(viteBase(basePath)).toBe(basePath);
  });

  it('preserves the root base path', () => {
    expect(viteBase(normalizeBasePath('/'))).toBe('/');
  });
});
