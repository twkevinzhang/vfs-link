import { ObjectEntity, SessionEntity } from '@site/models';
import axios, { AxiosInstance } from 'axios';

export interface ObjectService {
  get({
    session,
    path,
  }: {
    session: SessionEntity;
    path: string;
  }): Promise<ObjectEntity>;

  getUploadSignedUrl({
    session,
    path,
  }: {
    session: SessionEntity;
    path: string;
  }): Promise<string>;

  getDownloadSignedUrl({
    session,
    path,
  }: {
    session: SessionEntity;
    path: string;
  }): Promise<string>;
}

export class ObjectServiceImpl implements ObjectService {
  private axios: AxiosInstance;

  constructor() {
    const baseUrl = import.meta.env.VITE_GCS_PROXY;
    if (!baseUrl || (typeof baseUrl === 'string' && isEmpty(baseUrl))) {
      throw new Error('VITE_GCS_PROXY is not defined');
    }
    this.axios = axios.create({
      baseURL: baseUrl,
      timeout: 3000,
    });
  }

  async get({
    session,
    path,
  }: {
    session: SessionEntity;
    path: string;
  }): Promise<ObjectEntity> {
    throw new Error('Not implemented');
  }

  async getUploadSignedUrl({
    session,
    path,
  }: {
    session: SessionEntity;
    path: string;
  }): Promise<string> {
    throw new Error('Not implemented');
  }

  async getDownloadSignedUrl({
    session,
    path,
  }: {
    session: SessionEntity;
    path: string;
  }): Promise<string> {
    throw new Error('Not implemented');
  }
}
