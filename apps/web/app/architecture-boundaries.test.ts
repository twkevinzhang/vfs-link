import { spawnSync } from 'node:child_process';
import {
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { dirname, join, relative } from 'node:path';

import { afterAll, describe, expect, it } from 'vitest';

const appRoot = join(process.cwd(), 'app');
const fixtureWorkspaceRoot = join(process.cwd(), '.architecture-fixtures');
const dependencyCruiserTimeoutMs = 20_000;

afterAll(() => {
  rmSync(fixtureWorkspaceRoot, { recursive: true, force: true });
});

function productionSources(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return productionSources(path);
    return /[.](?:ts|tsx)$/.test(entry.name) &&
      !/[.](?:test|spec)[.](?:ts|tsx)$/.test(entry.name)
      ? [path]
      : [];
  });
}

function fixtureName() {
  return `__boundary_${process.pid}_${Date.now()}_${Math.random()
    .toString(16)
    .slice(2)}`;
}

function fixtureAppRoot(fixture: string) {
  return join(fixtureWorkspaceRoot, fixture, 'app');
}

function assertDependencyViolation({
  source,
  target,
  expectedRule,
  cleanup,
}: {
  source: string;
  target: string;
  expectedRule: string;
  cleanup: string[];
}) {
  mkdirSync(dirname(source), { recursive: true });
  mkdirSync(dirname(target), { recursive: true });
  const targetImport = relative(dirname(source), target).replaceAll('\\', '/');
  writeFileSync(
    source,
    `import '${
      targetImport.startsWith('.') ? targetImport : `./${targetImport}`
    }'; export const value = 1;\n`
  );
  writeFileSync(target, 'export {};\n');

  try {
    const result = spawnSync(
      'pnpm',
      [
        'exec',
        'depcruise',
        '--config',
        'dependency-cruiser.config.cjs',
        '--output-type',
        'err',
        relative(process.cwd(), source),
      ],
      { cwd: process.cwd(), encoding: 'utf8' }
    );
    expect(result.status).toBe(1);
    expect(`${result.stdout}\n${result.stderr}`).toContain(expectedRule);
  } finally {
    for (const path of cleanup) rmSync(path, { recursive: true, force: true });
  }
}

function assertLayerViolation(
  sourceLayer: string,
  targetLayer: string,
  expectedRule: string
) {
  const fixture = fixtureName();
  const workspace = join(fixtureWorkspaceRoot, fixture);
  const root = join(fixtureAppRoot(fixture), 'features', fixture);
  const source = join(root, sourceLayer, 'violation.js');
  const target = join(root, targetLayer, 'target.js');
  assertDependencyViolation({
    source,
    target,
    expectedRule,
    cleanup: [workspace],
  });
}

describe('frontend architecture boundaries', () => {
  it('has no obsolete top-level business directories or facades', () => {
    for (const directory of ['hooks', 'components', 'lib', 'types']) {
      expect(existsSync(join(appRoot, directory))).toBe(false);
    }
  });

  it('contains no durable browser queue APIs', () => {
    const sources = productionSources(appRoot)
      .map((path) => readFileSync(path, 'utf8'))
      .join('\n');
    expect(sources).not.toMatch(
      /indexedDB|\bIDB[A-Z]|BroadcastChannel|navigator[.]locks|FileSystemFileHandle|showOpenFilePicker|showDirectoryPicker|storage[.]getDirectory|\bOPFS\b/
    );
  });

  it('keeps domain and application browser and framework neutral', () => {
    const sources = productionSources(join(appRoot, 'features'))
      .filter((path) => /\/(?:domain|application)\//.test(path))
      .map(
        (path) => `${relative(appRoot, path)}\n${readFileSync(path, 'utf8')}`
      )
      .join('\n');
    expect(sources).not.toMatch(
      /from ['"](?:react|react-router)|\b(?:File|Blob|AbortSignal|XMLHttpRequest|localStorage|sessionStorage|indexedDB|window|document|navigator)\b|\bfetch\s*\(/
    );
  });

  it('places every feature production module in one of the five layers', () => {
    const featureRoot = join(appRoot, 'features');
    for (const path of productionSources(featureRoot)) {
      expect(relative(featureRoot, path).replaceAll('\\', '/')).toMatch(
        /^[^/]+\/(?:domain|application|infrastructure|presentation|composition)\//
      );
    }
  });

  it(
    'rejects domain to presentation',
    () => {
      assertLayerViolation('domain', 'presentation', 'domain-allowlist');
    },
    dependencyCruiserTimeoutMs
  );

  it(
    'rejects presentation to infrastructure',
    () => {
      assertLayerViolation(
        'presentation',
        'infrastructure',
        'presentation-allowlist'
      );
    },
    dependencyCruiserTimeoutMs
  );

  it(
    'rejects application to infrastructure',
    () => {
      assertLayerViolation(
        'application',
        'infrastructure',
        'application-allowlist'
      );
    },
    dependencyCruiserTimeoutMs
  );

  it(
    'rejects infrastructure to presentation',
    () => {
      assertLayerViolation(
        'infrastructure',
        'presentation',
        'infrastructure-allowlist'
      );
    },
    dependencyCruiserTimeoutMs
  );

  it(
    'rejects dependencies between newly added bounded contexts',
    () => {
      const fixture = fixtureName();
      const sourceContext = `${fixture}_source`;
      const workspace = join(fixtureWorkspaceRoot, fixture);
      const featureRoot = join(fixtureAppRoot(fixture), 'features');
      const sourceRoot = join(featureRoot, sourceContext);
      const targetRoot = join(featureRoot, `${fixture}_target`);
      assertDependencyViolation({
        source: join(sourceRoot, 'application', 'violation.js'),
        target: join(targetRoot, 'domain', 'target.js'),
        expectedRule: `${sourceContext}-bounded-context-isolated`,
        cleanup: [workspace],
      });
    },
    dependencyCruiserTimeoutMs
  );

  it(
    'rejects routes that import context internals',
    () => {
      const fixture = fixtureName();
      const workspace = join(fixtureWorkspaceRoot, fixture);
      const fixtureApp = fixtureAppRoot(fixture);
      const source = join(fixtureApp, 'routes', `${fixture}.js`);
      const targetRoot = join(fixtureApp, 'features', fixture);
      assertDependencyViolation({
        source,
        target: join(targetRoot, 'domain', 'target.js'),
        expectedRule: 'route-composition-roots-only',
        cleanup: [workspace],
      });
    },
    dependencyCruiserTimeoutMs
  );

  it(
    'rejects feature production modules outside the five layers',
    () => {
      const fixture = fixtureName();
      const workspace = join(fixtureWorkspaceRoot, fixture);
      const root = join(fixtureAppRoot(fixture), 'features', fixture);
      assertDependencyViolation({
        source: join(root, 'violation.js'),
        target: join(root, 'domain', 'target.js'),
        expectedRule: 'feature-production-files-belong-to-a-layer',
        cleanup: [workspace],
      });
    },
    dependencyCruiserTimeoutMs
  );
});
