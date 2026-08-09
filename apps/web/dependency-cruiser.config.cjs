/** @type {import('dependency-cruiser').IConfiguration} */
module.exports = {
  forbidden: [
    {
      name: 'no-production-cycles',
      severity: 'error',
      comment: 'Production modules must remain acyclic.',
      from: {
        path: '^app/',
        pathNot: '[.](?:test|spec)[.](?:ts|tsx)$',
      },
      to: {
        circular: true,
        pathNot: '[.](?:test|spec)[.](?:ts|tsx)$',
      },
    },
    {
      name: 'no-unresolved-local-imports',
      severity: 'error',
      from: { path: '^app/' },
      to: { couldNotResolve: true, path: '^app/' },
    },
    {
      name: 'application-does-not-import-presentation',
      severity: 'error',
      comment:
        'Application contracts own workflow inputs; presentation modules consume them.',
      from: { path: '^app/features/[^/]+/application/' },
      to: { path: '^app/features/[^/]+/presentation/' },
    },
    {
      name: 'application-does-not-import-infrastructure',
      severity: 'error',
      comment:
        'Application owns ports and use cases; infrastructure implements those ports.',
      from: { path: '^app/features/[^/]+/application/' },
      to: {
        path: [
          '^app/features/[^/]+/infrastructure/',
          '^app/shared/infrastructure/',
          '^app/(?:components|hooks|routes|lib/api)',
        ],
      },
    },
    {
      name: 'domain-does-not-import-outer-layers',
      severity: 'error',
      comment:
        'Domain modules may only depend on their domain peers and the shared kernel.',
      from: { path: '^app/features/[^/]+/domain/' },
      to: {
        path: [
          '^app/features/[^/]+/(?:application|infrastructure|presentation)/',
          '^app/(?:components|hooks|routes|lib/api|shared/infrastructure)',
        ],
      },
    },
    {
      name: 'infrastructure-does-not-import-presentation',
      severity: 'error',
      from: { path: '^app/features/[^/]+/infrastructure/' },
      to: {
        path: [
          '^app/features/[^/]+/presentation/',
          '^app/(?:components|hooks|routes)',
        ],
      },
    },
    {
      name: 'presentation-and-hooks-use-injected-infrastructure',
      severity: 'error',
      comment:
        'React adapters receive application-owned dependencies from a route/root composition root.',
      from: {
        path: '^app/(?:components/|hooks/|features/[^/]+/presentation/)',
      },
      to: {
        path: [
          '^app/features/[^/]+/infrastructure/',
          '^app/lib/archive-temporary-storage[.]ts$',
        ],
      },
    },
    {
      name: 'upload-context-isolated',
      severity: 'error',
      from: { path: '^app/features/upload/' },
      to: { path: '^app/features/(?:files|drift|share)/' },
    },
    {
      name: 'files-context-isolated',
      severity: 'error',
      from: { path: '^app/features/files/' },
      to: { path: '^app/features/(?:drift|share)/' },
    },
    {
      name: 'drift-context-isolated',
      severity: 'error',
      from: { path: '^app/features/drift/' },
      to: { path: '^app/features/(?:files|upload|share)/' },
    },
    {
      name: 'share-context-isolated',
      severity: 'error',
      from: { path: '^app/features/share/' },
      to: { path: '^app/features/(?:files|upload|drift)/' },
    },
    {
      name: 'production-does-not-use-api-compatibility-facade',
      severity: 'error',
      comment: 'The api.ts barrel exists for compatibility tests only.',
      from: {
        path: '^app/',
        pathNot: '[.](?:test|spec)[.](?:ts|tsx)$',
      },
      to: { path: '^app/lib/api[.]ts$' },
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
