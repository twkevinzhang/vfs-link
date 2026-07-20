import { describe, expect, it } from 'vitest';

import {
  FILES_ROUTE,
  fileBrowserPath,
  logicalPathFromRoute,
} from './file-route';

describe('file browser routes', () => {
  it('uses /files for the logical root', () => {
    expect(fileBrowserPath('/')).toBe(FILES_ROUTE);
    expect(logicalPathFromRoute(undefined)).toBe('/');
  });

  it('keeps nested logical path segments in the URL', () => {
    expect(fileBrowserPath('/AHR/video')).toBe('/files/AHR/video');
    expect(logicalPathFromRoute('AHR/video')).toBe('/AHR/video');
  });

  it('encodes each special-character segment exactly once', () => {
    expect(fileBrowserPath('/測試 folder/#?%')).toBe(
      '/files/%E6%B8%AC%E8%A9%A6%20folder/%23%3F%25'
    );
    expect(logicalPathFromRoute('測試 folder/#?%')).toBe('/測試 folder/#?%');
  });
});
