import { nanoid } from 'nanoid';
import { Driver, EntityPath, ObjectMimeType } from '.';
import { Auth } from '@site/services/auth';

export interface BucketEntity {
  name: string;
}

export class SessionEntity {
  private constructor(
    public readonly id: string,
    public readonly name: string,
    public readonly description: string | undefined,
    public readonly driver: Driver,
    public readonly auth: Auth,
    public readonly mount: string,
    public readonly uploadedAtISO: string,
    public readonly latestConnectedISO: string | undefined,
    public readonly metadataPath: string
  ) {}

  static new(params: {
    name?: string;
    description?: string;
    driver: Driver;
    auth: Auth;
    mount: string;
    uploadedAtISO?: string;
    latestConnectedISO?: string;
    metadataPath?: string;
  }): SessionEntity {
    return new SessionEntity(
      nanoid(6),
      params.name ?? 'Untitled',
      params.description,
      params.driver,
      params.auth,
      params.mount,
      params.uploadedAtISO ?? new Date().toISOString(),
      params.latestConnectedISO,
      params.metadataPath ?? '/'
    );
  }

  get createdAt() {
    return new Date(this.uploadedAtISO);
  }
  get latestConnectedAt() {
    return this.latestConnectedISO
      ? new Date(this.latestConnectedISO)
      : undefined;
  }
}

export interface FlatObject {
  readonly path: EntityPath;
  readonly pathOnDrive: string;
  readonly mimeType?: ObjectMimeType;
  readonly sizeBytes?: number;
  readonly uploadedAtISO?: string;
  readonly latestUpdatedAtISO?: string;
  readonly md5Hash?: string;
  readonly crc32c?: string;
  readonly xxHash64?: string;
  readonly deletedAtISO?: string;
}

export class ObjectEntity implements FlatObject {
  private constructor(
    public readonly path: EntityPath,
    public readonly pathOnDrive: string,
    public readonly mimeType?: ObjectMimeType,
    public readonly sizeBytes?: number,
    public readonly uploadedAtISO?: string,
    public readonly latestUpdatedAtISO?: string,
    public readonly md5Hash?: string,
    public readonly crc32c?: string,
    public readonly xxHash64?: string,
    public readonly deletedAtISO?: string
  ) {}

  static new(params: {
    path: EntityPath;
    pathOnDrive: string;
    mimeType?: ObjectMimeType;
    sizeBytes?: number;
    uploadedAtISO?: string;
    latestUpdatedAtISO?: string;
    md5Hash?: string;
    crc32c?: string;
    xxHash64?: string;
    deletedAtISO?: string;
  }): ObjectEntity {
    return new ObjectEntity(
      params.path,
      params.pathOnDrive,
      params.mimeType,
      params.sizeBytes,
      params.uploadedAtISO,
      params.latestUpdatedAtISO,
      params.md5Hash,
      params.crc32c,
      params.xxHash64,
      params.deletedAtISO
    );
  }

  static fromJson(json: any, sessionId: string): ObjectEntity {
    return ObjectEntity.new({
      ...json,
      path: EntityPath.fromRoute({
        sessionId: sessionId,
        mount: json.path,
      }),
      pathOnDrive: json.pathOnDrive,
      mimeType: json.mimeType,
      sizeBytes: json.sizeBytes,
      uploadedAtISO: json.uploadedAtISO,
      latestUpdatedAtISO: json.latestUpdatedAtISO,
      md5Hash: json.md5Hash,
      crc32c: json.crc32c,
      xxHash64: json.xxHash64,
      deletedAtISO: json.deletedAtISO,
    });
  }

  // GCS File response doc: https://github.com/googleapis/nodejs-storage/blob/3dcda1b7153664197215c7316761e408ca870bc4/src/file.ts#L556
  static fromGCS(f: any, sessionId: string): ObjectEntity {
    const isFolder = f.metadata.name.endsWith('/');
    const userPath = f.metadata.metadata?.userPath || f.metadata.name;
    return ObjectEntity.new({
      ...f,
      ...f.metadata,
      name: getFilename(userPath),
      path: EntityPath.fromRoute({
        sessionId,
        mount: removeLeadingSlash(removeTrailingSlash(userPath)),
      }),
      pathOnDrive: f.metadata.name,
      mimeType: isFolder ? ObjectMimeType.folder : f.metadata.contentType,
      sizeBytes: f.metadata.size ? Number(f.metadata.size) : undefined,
      uploadedAtISO: f.metadata.timeCreated,
      latestUpdatedAtISO: f.metadata.updated,
      md5Hash: f.metadata.md5Hash,
      crc32c: f.metadata.crc32c,
      xxHash64: f.metadata.metadata?.xxHash64,
      // deletedAtISO: f.metadata.timeDeleted,
    });
  }

  toJson(): string {
    return JSON.stringify({
      path: this.path.toSegmentsString(),
      pathOnDrive: this.pathOnDrive,
      mimeType: this.mimeType,
      sizeBytes: this.sizeBytes,
      uploadedAtISO: this.uploadedAtISO,
      latestUpdatedAtISO: this.latestUpdatedAtISO,
      md5Hash: this.md5Hash,
      crc32c: this.crc32c,
      xxHash64: this.xxHash64,
      deletedAtISO: this.deletedAtISO,
    });
  }

  static ArrayfromJson(json: string, sessionId: string): ObjectEntity[] {
    return JSON.parse(json).map((item: any) => this.fromJson(item, sessionId));
  }

  get name(): string {
    return this.path.name;
  }

  get isFolder(): boolean {
    return this.mimeType === ObjectMimeType.folder;
  }

  get createdAt(): Date | undefined {
    return this.uploadedAtISO ? new Date(this.uploadedAtISO) : undefined;
  }

  get latestUpdatedAt(): Date | undefined {
    return this.latestUpdatedAtISO
      ? new Date(this.latestUpdatedAtISO)
      : undefined;
  }

  get sizeFormatted(): string {
    if (!this.sizeBytes) return '';
    const units = ['B', 'KB', 'MB', 'GB'];
    let size = Number(this.sizeBytes);
    let i = 0;
    while (size >= 1024 && i < latestIndex(units)) {
      size /= 1024;
      i++;
    }
    return `${size.toFixed(1)} ${units[i]}`;
  }

  get modifiedAtFormatted(): string {
    if (!this.latestUpdatedAtISO) return '-';
    return new Date(this.latestUpdatedAtISO).toLocaleString();
  }

  get deletedAt(): Date | undefined {
    return this.deletedAtISO ? new Date(this.deletedAtISO) : undefined;
  }
}

// export interface ExpandedFile {
//   entity: ObjectEntity;
//   relativePath: string;
// }

// /**
//  * Recursively expand a folder to get all file entities
//  */
// export function expandFolder(
//   folder: ObjectEntity,
//   allEntities: ObjectEntity[],
//   basePath: string = ''
// ): ExpandedFile[] {
//   const result: ExpandedFile[] = [];
//   const folderPathStr = folder.path.toString();

//   for (const entity of allEntities) {
//     const entityPathStr = entity.path.toString();

//     // Check if entity is inside this folder
//     if (entityPathStr.startsWith(folderPathStr + '/')) {
//       const relativePath = entityPathStr.substring(folderPathStr.length + 1);

//       if (entity.isFolder) {
//         // Recursively expand subfolders
//         result.push(
//           ...expandFolder(
//             entity,
//             allEntities,
//             basePath ? `${basePath}/${relativePath}` : relativePath
//           )
//         );
//       } else {
//         // Add file with relative path
//         result.push({
//           entity,
//           relativePath: basePath ? `${basePath}/${relativePath}` : relativePath,
//         });
//       }
//     }
//   }

//   return result;
// }

// /**
//  * Expand selections to include all files in folders
//  */
// export function expandSelections(
//   selectedEntities: ObjectEntity[],
//   allEntities: ObjectEntity[]
// ): ExpandedFile[] {
//   const result: ExpandedFile[] = [];

//   for (const entity of selectedEntities) {
//     if (!entity.isFolder) {
//       // Direct file selection
//       result.push({
//         entity,
//         relativePath: entity.name,
//       });
//     } else {
//       // Folder selection - expand to all files
//       result.push(...expandFolder(entity, allEntities));
//     }
//   }

//   return result;
// }
