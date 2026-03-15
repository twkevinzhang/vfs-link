import { Storage } from '@google-cloud/storage';
import { Pool } from 'pg';
import { PrismaPg } from '@prisma/adapter-pg';
import { PrismaClient } from '@prisma/client';
import * as dotenv from 'dotenv';
import path from 'path';

// Load environment variables from .env
dotenv.config();

async function main() {
  // --- 1. Configuration & Argument Parsing ---
  const args = process.argv.slice(2);
  const argMap: Record<string, string> = {};
  args.forEach((arg) => {
    const [key, value] = arg.split('=');
    if (key && value) argMap[key] = value;
  });

  const GCS_BUCKET = argMap.GCS_BUCKET || process.env.GCS_BUCKET;
  const GOOGLE_APPLICATION_CREDENTIALS = 
    argMap.GOOGLE_APPLICATION_CREDENTIALS || 
    process.env.GOOGLE_APPLICATION_CREDENTIALS || 
    '.auth/gcp-key.json';
  const DATABASE_URL = process.env.DATABASE_URL;

  if (!GCS_BUCKET) {
    console.error('❌ Error: GCS_BUCKET is not specified (env or arg).');
    process.exit(1);
  }

  if (!DATABASE_URL) {
    console.error('❌ Error: DATABASE_URL is not set in environment.');
    process.exit(1);
  }

  // Set credentials for GCS SDK
  process.env.GOOGLE_APPLICATION_CREDENTIALS = path.resolve(GOOGLE_APPLICATION_CREDENTIALS);

  console.log('--- Configuration ---');
  console.log(`Bucket: ${GCS_BUCKET}`);
  console.log(`Credentials: ${process.env.GOOGLE_APPLICATION_CREDENTIALS}`);
  console.log(`Database: ${DATABASE_URL.replace(/:[^:]+@/, ':****@')}`); // Mask password
  console.log('---------------------\n');

  // --- 2. 5-Second Countdown ---
  console.log('⚠️  Warning: This will rebuild the mapping table based on GCS content.');
  for (let i = 5; i > 0; i--) {
    process.stdout.write(`Starting in ${i} seconds... \r`);
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  console.log('\n🚀 Starting rebuild process...\n');

  // --- 3. Initialization ---
  const storage = new Storage();
  const bucket = storage.bucket(GCS_BUCKET);
  const pool = new Pool({ connectionString: DATABASE_URL });
  const adapter = new PrismaPg(pool as any);
  const prisma = new PrismaClient({ adapter } as any);

  try {
    // --- 4. GCS Bucket Listing ---
    console.log(`Listing objects in bucket: ${GCS_BUCKET}...`);
    const [files] = await bucket.getFiles();
    console.log(`Found ${files.length} objects.\n`);

    let processedCount = 0;

    for (const file of files) {
      const physicalHash = file.name;
      const size = BigInt(file.metadata.size || 0);

      // logicPath logic:
      // If the filename looks like a UUID (as per gcs.ts), we might not know the original logicPath.
      // For this script, we'll assume the physicalHash is the filename.
      // If folder items exist, GCS usually represents them with a trailing slash or via delimiter.
      
      const isDirectory = physicalHash.endsWith('/');
      const logicPath = '/' + (isDirectory ? physicalHash.slice(0, -1) : physicalHash);

      console.log(`[${++processedCount}/${files.length}] Processing: ${physicalHash} -> ${logicPath}`);

      await prisma.file.upsert({
        where: { logicPath },
        update: {
          physicalHash,
          size,
          isDirectory,
          updatedAt: new Date(),
        },
        create: {
          logicPath,
          physicalHash,
          size,
          isDirectory,
        },
      });
    }

    console.log('\n✅ Rebuild completed successfully!');
  } catch (error) {
    console.error('\n❌ Rebuild failed:', error);
  } finally {
    await prisma.$disconnect();
    await pool.end();
  }
}

main();
