import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

import { createRequestHandler } from 'react-router';

const buildFile = path.resolve('build/server/index.js');
const build = await import(`${pathToFileURL(buildFile).href}?t=${Date.now()}`);
const basename = normalizeBasePath(build.basename);
const requestPath = basename === '/' ? '/' : basename;
const handler = createRequestHandler(build, 'production');
const response = await handler(
  new Request(`http://localhost${requestPath}`, {
    headers: { 'X-React-Router-SPA-Mode': 'yes' },
  })
);
const html = await response.text();

if (response.status !== 200) {
  throw new Error(
    `Unable to generate SPA fallback: received ${response.status} ${response.statusText}\n${html}`
  );
}

if (
  !html.includes('window.__reactRouterContext =') ||
  !html.includes('window.__reactRouterRouteModules =')
) {
  throw new Error('Unable to generate SPA fallback: missing hydration scripts');
}

await mkdir(path.resolve('build/client'), { recursive: true });
await writeFile(path.resolve('build/client/index.html'), html);
console.log(`SPA Mode: Generated build/client/index.html from ${requestPath}`);

function normalizeBasePath(value) {
  const trimmed = value?.trim();
  if (!trimmed || trimmed === '/') {
    return '/';
  }
  const withoutSlashes = trimmed.replace(/^\/+|\/+$/g, '');
  return withoutSlashes ? `/${withoutSlashes}` : '/';
}
