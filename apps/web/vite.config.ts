import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { reactRouter } from '@react-router/dev/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';
import tsconfigPaths from 'vite-tsconfig-paths';

import { normalizeBasePath, viteBase } from './base-path.config';

const currentDir = dirname(fileURLToPath(import.meta.url));
const basePath = normalizeBasePath(process.env.VITE_BASE_PATH);

export default defineConfig({
  root: currentDir,
  base: viteBase(basePath),
  plugins: [
    tailwindcss(),
    reactRouter(),
    tsconfigPaths({ projects: [resolve(currentDir, 'tsconfig.json')] }),
  ],
  server: {
    port: 5173,
  },
});
