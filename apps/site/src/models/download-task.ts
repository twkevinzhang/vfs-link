export enum DownloadStatus {
  PENDING = 'pending',
  FETCHING_URL = 'fetching_url',
  DOWNLOADING = 'downloading',
  PAUSED = 'paused',
  COMPLETED = 'completed',
  FAILED = 'failed',
  EXPIRED = 'expired',
  CANCELLED = 'cancelled',
}

export interface DownloadTaskFile {
  name: string;
  pathOnDrive: string;
  size: number;
}

export class DownloadTask {
  private constructor(
    public id: string,
    public sessionId: string,
    public file: DownloadTaskFile,
    public downloadedBytes: number,
    public status: DownloadStatus,
    public priority: number,
    public createdAt: string,
    public updatedAt: string,
    public signedUrl?: string,
    public urlExpiresAt?: string,
    public error?: string,
    public blobUrl?: string,
    public speed?: number,
    public eta?: number
  ) {}

  static new(params: {
    sessionId: string;
    file: DownloadTaskFile;
    priority?: number;
  }): DownloadTask {
    return new DownloadTask(
      crypto.randomUUID(),
      params.sessionId,
      params.file,
      0,
      DownloadStatus.PENDING,
      params.priority ?? Date.now(),
      new Date().toISOString(),
      new Date().toISOString()
    );
  }

  static merge(
    source: DownloadTask,
    updates: Partial<DownloadTask>
  ): DownloadTask {
    return new DownloadTask(
      source.id,
      source.sessionId,
      updates.file ?? source.file,
      updates.downloadedBytes ?? source.downloadedBytes,
      updates.status ?? source.status,
      updates.priority ?? source.priority,
      source.createdAt,
      new Date().toISOString(),
      updates.signedUrl ?? source.signedUrl,
      updates.urlExpiresAt ?? source.urlExpiresAt,
      updates.error ?? source.error,
      updates.blobUrl ?? source.blobUrl,
      updates.speed ?? source.speed,
      updates.eta ?? source.eta
    );
  }

  toJson(): string {
    return JSON.stringify({
      id: this.id,
      sessionId: this.sessionId,
      file: this.file,
      downloadedBytes: this.downloadedBytes,
      status: this.status,
      priority: this.priority,
      createdAt: this.createdAt,
      updatedAt: this.updatedAt,
      signedUrl: this.signedUrl,
      urlExpiresAt: this.urlExpiresAt,
      error: this.error,
    });
  }

  static fromJson(json: string): DownloadTask {
    const obj = JSON.parse(json);
    return new DownloadTask(
      obj.id,
      obj.sessionId,
      obj.file,
      obj.downloadedBytes,
      obj.status,
      obj.priority,
      obj.createdAt,
      obj.updatedAt,
      obj.signedUrl,
      obj.urlExpiresAt,
      obj.error
    );
  }
}
