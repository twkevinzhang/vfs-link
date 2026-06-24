import type { Config } from '@react-router/dev/config';

import { normalizeBasePath } from './base-path.config';

const basename = normalizeBasePath(process.env.VITE_BASE_PATH);

export default {
  ssr: false,
  basename,
  routeDiscovery: { mode: 'initial' },
} satisfies Config;
