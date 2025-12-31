import { Storage, File, Bucket } from '@google-cloud/storage';

export interface ServiceAccountJson {
  type?: string;
  project_id?: string;
  private_key_id?: string;
  private_key?: string;
  client_email?: string;
  client_id?: string;
  auth_uri?: string;
  token_uri?: string;
  auth_provider_x509_cert_url?: string;
  client_x509_cert_url?: string;
  universe_domain?: string;
}

export interface GcsRequest {
  auth: ServiceAccountJson;
  bucket?: string;
  file?: string;
  method: string;
  args: any[];
}

export class GcsExecutor {
  static async execute(req: GcsRequest) {
    const serviceAccountJson = req.auth;
    const storage = new Storage({
      projectId: serviceAccountJson?.project_id,
      credentials: serviceAccountJson,
    });

    let target: any = storage;
    if (req.bucket) {
      target = target.bucket(req.bucket);
      if (req.file) {
        target = target.file(req.file);
      }
    }

    if (typeof target[req.method] !== 'function') {
      throw new Error(`Method ${req.method} not found on target`);
    }

    const result = await target[req.method](...req.args);
    return this.sanitize(result);
  }

  private static sanitize(obj: any): any {
    if (Array.isArray(obj)) {
      return obj.map((item) => this.sanitize(item));
    }

    if (obj instanceof File) {
      return {
        name: obj.name,
        bucket: obj.bucket.name,
        metadata: obj.metadata,
      };
    }

    if (obj instanceof Bucket) {
      return {
        name: obj.name,
        metadata: obj.metadata,
      };
    }

    if (obj !== null && typeof obj === 'object') {
      if (Buffer.isBuffer(obj)) {
        return obj;
      }
      // If it's a simple object, return it but be careful with circularity
      // For now, let's just return it and see what happens.
      // But we should strip out things that are clearly SDK internal classes.
      const sanitized: any = {};
      for (const key in obj) {
        if (Object.prototype.hasOwnProperty.call(obj, key)) {
          const val = obj[key];
          if (typeof val === 'function') continue;
          if (key === 'storage' || key === 'bucket' || key === 'file') continue; // Strip circular SDK refs
          sanitized[key] = val;
        }
      }
      return sanitized;
    }

    return obj;
  }
}
