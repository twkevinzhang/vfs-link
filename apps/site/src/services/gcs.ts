import axios from 'axios';

// from service account json
export interface GcsAuth {
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

export class GcsProxyClient {
  private baseUrl: string;

  constructor(private auth: GcsAuth) {
    this.baseUrl = import.meta.env.VITE_GCS_PROXY.replace(/\/$/, '');
    if (!this.baseUrl || isEmpty(this.baseUrl)) {
      throw new Error('VITE_GCS_PROXY is not defined');
    }
  }

  bucket(name: string) {
    return new ProxyBucket(this.baseUrl, this.auth, name);
  }

  async getBuckets() {
    const res = await axios.post(`${this.baseUrl}/gcs/v1/execute`, {
      auth: this.auth,
      method: 'getBuckets',
      args: [],
    });
    return res.data.data;
  }
}

export class ProxyBucket {
  constructor(
    private baseUrl: string,
    private auth: GcsAuth,
    private name: string
  ) {}

  file(path: string) {
    return new ProxyFile(this.baseUrl, this.auth, this.name, path);
  }

  async getFiles(options?: any) {
    return this.execute('getFiles', [options]);
  }

  async delete() {
    return this.execute('delete', []);
  }

  async exists() {
    return this.execute('exists', []);
  }

  async setMetadata(metadata: any) {
    return this.execute('setMetadata', [metadata]);
  }

  async get() {
    return this.execute('get', []);
  }

  async download(options?: any) {
    return this.execute('download', [options]);
  }

  async save(data: any, options?: any) {
    return this.execute('save', [data, options]);
  }

  private async execute(method: string, args: any[]) {
    const res = await axios.post(`${this.baseUrl}/gcs/v1/execute`, {
      auth: this.auth,
      bucket: this.name,
      method,
      args,
    });
    return res.data.data;
  }
}

export class ProxyFile {
  constructor(
    private baseUrl: string,
    private auth: GcsAuth,
    private bucket: string,
    private path: string
  ) {}

  async getMetadata() {
    return this.execute('getMetadata', []);
  }

  async setMetadata(metadata: any) {
    return this.execute('setMetadata', [metadata]);
  }

  async delete() {
    return this.execute('delete', []);
  }

  async exists() {
    return this.execute('exists', []);
  }

  async move(destination: string | ProxyFile) {
    const destName =
      typeof destination === 'string' ? destination : destination.path;
    return this.execute('move', [destName]);
  }

  async get() {
    return this.execute('get', []);
  }

  async download(options?: any) {
    return this.execute('download', [options]);
  }

  async save(data: any, options?: any) {
    return this.execute('save', [data, options]);
  }

  async createResumableUpload(options?: any) {
    return this.execute('createResumableUpload', [options]);
  }

  async getSignedUrl(options?: {
    action?: 'read' | 'write' | 'delete' | 'resumable';
    expires?: number | string | Date;
    contentType?: string;
  }): Promise<string> {
    const result = await this.execute('getSignedUrl', [
      options || { action: 'read', expires: Date.now() + 3600000 },
    ]);
    // Result is an array with [url]
    return Array.isArray(result) ? result[0] : result;
  }

  /**
   * 通過 proxy 下載檔案（返回 download URL）
   */
  getProxyDownloadUrl(): string {
    const params = new URLSearchParams({
      bucket: this.bucket,
      file: this.path,
      projectId: this.auth.projectId || '',
      accessKey: this.auth.accessKey || '',
      secretKey: this.auth.secretKey || '',
    });
    return `${this.baseUrl}/gcs/v1/download?${params.toString()}`;
  }

  private async execute(method: string, args: any[]) {
    const res = await axios.post(`${this.baseUrl}/gcs/v1/execute`, {
      auth: this.auth,
      bucket: this.bucket,
      file: this.path,
      method,
      args,
    });
    return res.data.data;
  }
}
