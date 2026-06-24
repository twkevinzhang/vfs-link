import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { reactRouter } from '@react-router/dev/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';
import tsconfigPaths from 'vite-tsconfig-paths';

const currentDir = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  root: currentDir,
  plugins: [
    tailwindcss(),
    reactRouter(),
    tsconfigPaths({ projects: [resolve(currentDir, 'tsconfig.json')] }),
  ],
  server: {
    port: 5173,
  },
});
