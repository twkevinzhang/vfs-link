import axios, { AxiosInstance } from 'axios';

export interface HmacAuth {
  accessKey?: string;
  secretKey?: string;
}

export class S3Service {
  private axios: AxiosInstance;

  private constructor(
    public readonly accessKey: string,
    public readonly secretKey: string,
    public readonly projectId: string
  ) {
    this.axios = axios.create({
      timeout: 3000,
      headers: {
        Authorization: accessKey,
      },
    });
  }

  static new({
    accessKey,
    secretKey,
    projectId,
  }: {
    accessKey: string;
    secretKey: string;
    projectId: string;
  }) {
    return new S3Service(accessKey, secretKey, projectId);
  }

  async listBuckets(): Promise<string[]> {
    throw new Error('Method not implemented.');
  }
}
