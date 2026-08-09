import { apiUrl } from '../../../shared/infrastructure/http/http-client';
import type { BrowserUploadSourceRegistry } from './browser-upload-source-registry';

export function createUploadThumbnailGateway(
  sources: BrowserUploadSourceRegistry
) {
  return {
    async save(input: {
      paths: string[];
      sourceId: string;
      width: number;
      height: number;
    }) {
      const body = new FormData();
      body.set('paths', JSON.stringify(input.paths));
      body.set('width', String(input.width));
      body.set('height', String(input.height));
      body.set('thumbnail', sources.get(input.sourceId), 'thumbnail.webp');
      const response = await fetch(apiUrl('/api/thumbnails'), {
        method: 'POST',
        headers: { Accept: 'application/json' },
        body,
      });
      if (!response.ok)
        throw new Error(`${response.status} ${response.statusText}`);
    },
    async clear(paths: string[]) {
      const response = await fetch(apiUrl('/api/thumbnails'), {
        method: 'DELETE',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ paths }),
      });
      if (!response.ok)
        throw new Error(`${response.status} ${response.statusText}`);
    },
  };
}
