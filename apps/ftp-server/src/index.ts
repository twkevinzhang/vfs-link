import 'dotenv/config';
import FtpServer from 'ftp-srv';
import { DatabaseFileSystem } from './vfs/database-vfs.js';

const {
  FTP_PORT = '21',
  FTP_USER = 'admin',
  FTP_PASS = 'admin123',
  FTP_PASV_URL = '127.0.0.1',
  FTP_PASV_MIN = '30000',
  FTP_PASV_MAX = '30005',
} = process.env;

const server = new FtpServer({
  url: `ftp://0.0.0.0:${FTP_PORT}`,
  pasv_url: FTP_PASV_URL,
  pasv_min: parseInt(FTP_PASV_MIN),
  pasv_max: parseInt(FTP_PASV_MAX),
  greeting: 'Welcome to vfs-link FTP Server',
});

server.on('login', ({ connection, username, password }, resolve, reject) => {
  console.log(`Login attempt: ${username}`);

  // 簡易驗證邏輯
  if (username === FTP_USER && password === FTP_PASS) {
    // 注入自定義的 DatabaseFileSystem
    return resolve({
      fs: new DatabaseFileSystem(connection, {
        root: '/',
        cwd: '/',
      }),
    });
  }

  return reject(new Error('Invalid username or password'));
});

server.on('client-error', ({ context, error }) => {
  console.error(`Client error: ${context} - ${error.message}`);
});

const start = async () => {
  try {
    await server.listen();
    console.log(`FTP Server is listening on port ${FTP_PORT}`);
  } catch (err) {
    console.error('Failed to start FTP server:', err);
    process.exit(1);
  }
};

start();
