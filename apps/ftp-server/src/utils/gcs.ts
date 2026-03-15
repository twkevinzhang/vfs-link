import { Storage } from '@google-cloud/storage';
import { v4 as uuidv4 } from 'uuid';
import path from 'path';

export class GCSStorage {
  private storage: Storage;
  private bucketName: string;

  constructor() {
    this.storage = new Storage();
    this.bucketName = process.env.GCS_BUCKET || '';
    if (!this.bucketName) {
      throw new Error('GCS_BUCKET environment variable is not set');
    }
  }

  getBucket() {
    return this.storage.bucket(this.bucketName);
  }

  generatePhysicalHash(originalPath: string): string {
    const ext = path.extname(originalPath);
    return `${uuidv4()}${ext}`;
  }

  async uploadStream(
    physicalHash: string,
    stream: NodeJS.ReadableStream,
  ): Promise<void> {
    const file = this.getBucket().file(physicalHash);
    const writeStream = file.createWriteStream({
      resumable: false,
      gzip: true,
    });

    return new Promise((resolve, reject) => {
      stream.pipe(writeStream).on('finish', resolve).on('error', reject);
    });
  }

  downloadStream(physicalHash: string) {
    const file = this.getBucket().file(physicalHash);
    return file.createReadStream();
  }

  async deleteFile(physicalHash: string) {
    const file = this.getBucket().file(physicalHash);
    await file.delete({ ignoreNotFound: true });
  }
}

export const gcs = new GCSStorage();
