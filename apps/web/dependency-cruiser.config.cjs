const { existsSync, readdirSync } = require('node:fs');
const { join } = require('node:path');

const FEATURE_LAYERS =
  '(?:domain|application|infrastructure|presentation|composition)';
const APP_ROOT_PATTERN = '(?:app|[.]architecture-fixtures/[^/]+/app)';
const contextsIn = (root) =>
  existsSync(root)
    ? readdirSync(root, { withFileTypes: true })
        .filter((entry) => entry.isDirectory())
        .map((entry) => entry.name)
    : [];
const fixtureRoot = join(__dirname, '.architecture-fixtures');
const fixtureFeatureRoots = existsSync(fixtureRoot)
  ? readdirSync(fixtureRoot, { withFileTypes: true })
      .filter((entry) => entry.isDirectory())
      .map((entry) => join(fixtureRoot, entry.name, 'app/features'))
  : [];
const featureContexts = [
  ...new Set([
    ...contextsIn(join(__dirname, 'app/features')),
    ...fixtureFeatureRoots.flatMap(contextsIn),
  ]),
];
const escapeRegExp = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** @type {import('dependency-cruiser').IConfiguration} */
module.exports = {
  forbidden: [
    {
      name: 'no-production-cycles',
      severity: 'error',
      from: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: '[.](?:test|spec)[.](?:ts|tsx)$',
      },
      to: { circular: true, pathNot: '[.](?:test|spec)[.](?:ts|tsx)$' },
    },
    {
      name: 'no-unresolved-local-imports',
      severity: 'error',
      from: { path: `^${APP_ROOT_PATTERN}/` },
      to: { couldNotResolve: true, path: `^${APP_ROOT_PATTERN}/` },
    },
    ...featureContexts.map((context) => ({
      name: `${context}-bounded-context-isolated`,
      severity: 'error',
      from: {
        path: `^${APP_ROOT_PATTERN}/features/${escapeRegExp(context)}/`,
      },
      to: {
        path: `^${APP_ROOT_PATTERN}/features/(?!${escapeRegExp(
          context
        )}/)[^/]+/`,
      },
    })),
    {
      name: 'feature-production-files-belong-to-a-layer',
      severity: 'error',
      comment:
        'Every feature production module must live in one of the five layers.',
      from: {
        path: `^${APP_ROOT_PATTERN}/features/[^/]+/(?!${FEATURE_LAYERS}/)`,
      },
      to: {},
    },
    {
      name: 'domain-allowlist',
      severity: 'error',
      comment: 'Domain may depend only on its domain and shared kernel.',
      from: { path: `^${APP_ROOT_PATTERN}/features/[^/]+/domain/` },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: [
          `^${APP_ROOT_PATTERN}/features/[^/]+/domain/`,
          `^${APP_ROOT_PATTERN}/shared/kernel/`,
        ],
      },
    },
    {
      name: 'application-allowlist',
      severity: 'error',
      comment: 'Application owns use cases and ports; it is browser-neutral.',
      from: { path: `^${APP_ROOT_PATTERN}/features/[^/]+/application/` },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: [
          `^${APP_ROOT_PATTERN}/features/[^/]+/(?:application|domain)/`,
          `^${APP_ROOT_PATTERN}/shared/kernel/`,
        ],
      },
    },
    {
      name: 'infrastructure-allowlist',
      severity: 'error',
      comment: 'Infrastructure implements application ports and cannot see UI.',
      from: {
        path: `^${APP_ROOT_PATTERN}/features/[^/]+/infrastructure/`,
      },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: [
          `^${APP_ROOT_PATTERN}/features/[^/]+/(?:infrastructure|application|domain)/`,
          `^${APP_ROOT_PATTERN}/shared/(?:infrastructure|kernel)/`,
        ],
      },
    },
    {
      name: 'presentation-allowlist',
      severity: 'error',
      comment:
        'Presentation uses application/domain contracts and shared UI only.',
      from: { path: `^${APP_ROOT_PATTERN}/features/[^/]+/presentation/` },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: [
          `^${APP_ROOT_PATTERN}/features/[^/]+/(?:presentation|application|domain)/`,
          `^${APP_ROOT_PATTERN}/shared/(?:presentation|kernel)/`,
        ],
      },
    },
    {
      name: 'composition-allowlist',
      severity: 'error',
      comment: 'Composition is the sole wiring layer for concrete adapters.',
      from: { path: `^${APP_ROOT_PATTERN}/features/[^/]+/composition/` },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: [
          `^${APP_ROOT_PATTERN}/features/[^/]+/(?:composition|presentation|infrastructure|application|domain)/`,
          `^${APP_ROOT_PATTERN}/shared/`,
        ],
      },
    },
    {
      name: 'shared-kernel-allowlist',
      severity: 'error',
      from: { path: `^${APP_ROOT_PATTERN}/shared/kernel/` },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: `^${APP_ROOT_PATTERN}/shared/kernel/`,
      },
    },
    {
      name: 'shared-infrastructure-allowlist',
      severity: 'error',
      from: { path: `^${APP_ROOT_PATTERN}/shared/infrastructure/` },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: `^${APP_ROOT_PATTERN}/shared/(?:infrastructure|kernel)/`,
      },
    },
    {
      name: 'shared-presentation-allowlist',
      severity: 'error',
      from: { path: `^${APP_ROOT_PATTERN}/shared/presentation/` },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: `^${APP_ROOT_PATTERN}/shared/(?:presentation|kernel)/`,
      },
    },
    {
      name: 'route-composition-roots-only',
      severity: 'error',
      comment:
        'Routes and root import only context composition entries and shared adapters.',
      from: { path: `^${APP_ROOT_PATTERN}/(?:routes/|root[.]tsx$)` },
      to: {
        path: `^${APP_ROOT_PATTERN}/`,
        pathNot: [
          `^${APP_ROOT_PATTERN}/features/[^/]+/composition(?:/|$)`,
          `^${APP_ROOT_PATTERN}/shared/`,
          `^${APP_ROOT_PATTERN}/app[.]css$`,
        ],
      },
    },
  ],
  options: {
    doNotFollow: { path: 'node_modules' },
    exclude: { path: '[.](?:test|spec)[.](?:ts|tsx)$' },
    tsPreCompilationDeps: 'specify',
    tsConfig: { fileName: 'tsconfig.json' },
    enhancedResolveOptions: {
      extensions: ['.ts', '.tsx', '.js', '.jsx', '.json'],
    },
  },
};
