import { GcsAuth, GcsProxyClient } from './gcs';
import { HmacAuth } from './s3';
import { Driver, SessionEntity } from '@site/models';

export type Auth = GcsAuth | HmacAuth;

export function proxyBucket(session: SessionEntity) {
  if (session.driver === Driver.gcs) {
    const client = new GcsProxyClient(session.auth as GcsAuth);

    const bucket = client.bucket(removeLeadingSlash(session.mount));
    return bucket;
  } else {
    throw new Error(`Driver ${session.driver} not supported`);
  }
}

export function proxyMetadataFile(session: SessionEntity) {
  const bucket = proxyBucket(session);
  const metadataFile = bucket.file(removeLeadingSlash(session.metadataPath));
  return metadataFile;
}
