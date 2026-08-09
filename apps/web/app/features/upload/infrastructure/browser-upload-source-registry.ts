import type { UploadSourceDescriptor } from '../application/upload-contracts';

export class BrowserUploadSourceRegistry {
  private readonly sources = new Map<string, Blob>();

  register(
    source: unknown,
    metadata: Omit<UploadSourceDescriptor, 'sourceId'>
  ): UploadSourceDescriptor {
    if (!(source instanceof Blob)) {
      throw new TypeError('Browser upload sources must be Blob instances.');
    }
    const sourceId = crypto.randomUUID();
    this.sources.set(sourceId, source);
    return { sourceId, ...metadata };
  }

  get(sourceId: string) {
    const source = this.sources.get(sourceId);
    if (!source) throw new Error('Upload source is no longer available.');
    return source;
  }

  release(sourceId: string) {
    this.sources.delete(sourceId);
  }

  clear() {
    this.sources.clear();
  }
}
