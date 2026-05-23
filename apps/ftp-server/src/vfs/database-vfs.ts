import { FileSystem } from 'ftp-srv';
import { Pool } from 'pg';
import { PrismaPg } from '@prisma/adapter-pg';
import { PrismaClient } from '@prisma/client';
import { gcs } from '../utils/gcs.js';
import path from 'path';
import { Readable, Writable } from 'stream';

const pool = new Pool({ connectionString: process.env.DATABASE_URL });
const adapter = new PrismaPg(pool as any);
const prisma = new PrismaClient({ adapter } as any) as any;

export class DatabaseFileSystem extends FileSystem {
  constructor(connection: any, { root, cwd }: { root: string; cwd: string }) {
    super(connection, { root, cwd });
  }

  private getLogicPath(relativePath: string): string {
    if (path.posix.isAbsolute(relativePath)) {
      return path.posix.normalize(relativePath);
    }
    return path.posix.join(this.cwd, relativePath);
  }

  override async list(relativePath: string = '.') {
    const folderPath = this.getLogicPath(relativePath);

    // 查詢在該目錄下的所有檔案與子目錄
    // 邏輯：搜尋 logicPath 以 folderPath 開頭，但排除 folderPath 本身，且層級正確
    // 實作簡化：查詢所有以 folderPath/ 為前綴的記錄，且不再包含後續的 / (僅限第一層)

    const prefix = folderPath === '/' ? '/' : folderPath + '/';

    const files = await prisma.file.findMany({
      where: {
        logicPath: {
          startsWith: prefix,
        },
      },
    });

    // 這裡需要過濾出直接子項 (Level-1)
    // 範例：folderPath = /docs, prefix = /docs/
    // /docs/a.txt -> OK
    // /docs/sub/b.txt -> NOT OK (應該在列出 /docs/sub 時才出現)

    const result = files
      .filter((f) => {
        const subPath = f.logicPath.slice(prefix.length);
        return !subPath.includes('/');
      })
      .map((f) => ({
        name: f.logicPath.split('/').pop() || '',
        size: Number(f.size),
        mtime: f.updatedAt.getTime(),
        isDirectory: () => f.isDirectory,
        isFile: () => !f.isDirectory,
      }));

    return result;
  }

  override async chdir(relativePath: string) {
    const newPath = this.getLogicPath(relativePath);
    // 檢查目錄是否存在
    if (newPath !== '/') {
      const dir = await prisma.file.findUnique({
        where: { logicPath: newPath, isDirectory: true },
      });
      if (!dir) throw new Error(`Directory not found: ${newPath}`);
    }
    (this as any).cwd = newPath;
    return this.cwd;
  }

  override async read(relativePath: string) {
    const logicPath = this.getLogicPath(relativePath);
    const fileRecord = await prisma.file.findUnique({
      where: { logicPath, isDirectory: false },
    });

    if (!fileRecord) throw new Error(`File not found: ${logicPath}`);

    return gcs.downloadStream(fileRecord.physicalHash);
  }

  override async write(relativePath: string) {
    const logicPath = this.getLogicPath(relativePath);
    const physicalHash = gcs.generatePhysicalHash(logicPath);

    // 攔截寫入流
    const passThrough = new Readable({
      read() {},
    });

    // 實際的 GCS 上傳與 DB 更新邏輯在寫入完成後觸發
    // ftp-srv 的 write 需回傳一個 Writable stream
    // 我們實作一個代理流來監控進度或大小

    let size = 0;
    const gcsFile = gcs.getBucket().file(physicalHash);
    const gcsStream = gcsFile.createWriteStream({ resumable: false });

    gcsStream.on('finish', async () => {
      await prisma.file.upsert({
        where: { logicPath },
        update: {
          physicalHash,
          size: BigInt(size),
          updatedAt: new Date(),
        },
        create: {
          logicPath,
          physicalHash,
          size: BigInt(size),
          isDirectory: false,
        },
      });
    });

    const proxyStream = new Writable({
      write(chunk, encoding, callback) {
        size += chunk.length;
        gcsStream.write(chunk, encoding, callback);
      },
      final(callback) {
        gcsStream.end(callback);
      },
    });

    return proxyStream;
  }

  override async get(relativePath: string) {
    const logicPath = this.getLogicPath(relativePath);
    if (logicPath === '/') {
      return {
        name: '/',
        mode: 0o777,
        size: 0,
        mtime: Date.now(),
        isDirectory: () => true,
        isFile: () => false,
      };
    }

    const fileRecord = await prisma.file.findUnique({
      where: { logicPath },
    });

    if (!fileRecord) throw new Error(`no such file or directory: ${logicPath}`);

    return {
      name: path.posix.basename(logicPath),
      mode: fileRecord.isDirectory ? 0o777 : 0o666,
      size: Number(fileRecord.size),
      mtime: fileRecord.updatedAt.getTime(),
      isDirectory: () => fileRecord.isDirectory,
      isFile: () => !fileRecord.isDirectory,
    };
  }

  async stat(relativePath: string) {
    return this.get(relativePath);
  }

  override async rename(fromPath: string, toPath: string) {
    const fromLogicPath = this.getLogicPath(fromPath);
    const toLogicPath = this.getLogicPath(toPath);

    await prisma.$transaction(async (tx: any) => {
      const fromFile = await tx.file.findUnique({
        where: { logicPath: fromLogicPath },
      });

      if (!fromFile) throw new Error(`Source not found: ${fromLogicPath}`);

      // 更新目標項本身
      await tx.file.update({
        where: { logicPath: fromLogicPath },
        data: { logicPath: toLogicPath },
      });

      // 如果是目錄，需遞迴更新其下所有檔案的路徑
      if (fromFile.isDirectory) {
        const prefix = fromLogicPath.endsWith('/')
          ? fromLogicPath
          : fromLogicPath + '/';
        const newPrefix = toLogicPath.endsWith('/')
          ? toLogicPath
          : toLogicPath + '/';

        // 找出所有子項
        const children = await tx.file.findMany({
          where: {
            logicPath: {
              startsWith: prefix,
            },
          },
        });

        for (const child of children) {
          const relativePart = child.logicPath.slice(prefix.length);
          const newChildLogicPath = newPrefix + relativePart;

          await tx.file.update({
            where: { id: child.id },
            data: { logicPath: newChildLogicPath },
          });
        }
      }
    });
  }

  override async delete(relativePath: string) {
    const logicPath = this.getLogicPath(relativePath);
    const fileRecord = await prisma.file.findUnique({
      where: { logicPath },
    });

    if (!fileRecord) return;

    if (fileRecord.isDirectory) {
      const prefix = logicPath + '/';
      const children = await prisma.file.findMany({
        where: { logicPath: { startsWith: prefix } },
      });
      for (const child of children) {
        if (!child.isDirectory) {
          await gcs.deleteFile(child.physicalHash);
        }
      }
      await prisma.file.deleteMany({
        where: { logicPath: { startsWith: prefix } },
      });
    } else {
      await gcs.deleteFile(fileRecord.physicalHash);
    }

    await prisma.file.delete({ where: { logicPath } });
  }

  override async mkdir(relativePath: string) {
    const logicPath = this.getLogicPath(relativePath);
    await prisma.file.create({
      data: {
        logicPath,
        physicalHash: '', // 目錄不需要實體檔案
        isDirectory: true,
      },
    });
    return logicPath;
  }
}
