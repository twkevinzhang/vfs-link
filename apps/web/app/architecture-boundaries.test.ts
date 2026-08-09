import { spawnSync } from 'node:child_process';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { join, relative } from 'node:path';

import { describe, expect, it } from 'vitest';

describe('frontend architecture boundaries', () => {
  it('rejects a deliberate domain-to-presentation dependency', async () => {
    const context = `__boundary_fixture_${process.pid}_${Date.now()}`;
    const fixtureRoot = join(process.cwd(), 'app', 'features', context);
    const domainFile = join(fixtureRoot, 'domain', 'violation.js');
    const presentationFile = join(fixtureRoot, 'presentation', 'view.js');

    mkdirSync(join(fixtureRoot, 'domain'), { recursive: true });
    mkdirSync(join(fixtureRoot, 'presentation'), { recursive: true });
    writeFileSync(
      domainFile,
      "import { view } from '../presentation/view.js'; export { view };\n"
    );
    writeFileSync(presentationFile, "export const view = 'forbidden';\n");

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
          relative(process.cwd(), domainFile),
        ],
        { cwd: process.cwd(), encoding: 'utf8' }
      );
      expect(result.status).toBe(1);
      expect(`${result.stdout}\n${result.stderr}`).toContain(
        'domain-does-not-import-outer-layers'
      );
    } finally {
      rmSync(fixtureRoot, { recursive: true, force: true });
    }
  });

  it('rejects presentation code that directly imports infrastructure', () => {
    const context = `__composition_fixture_${process.pid}_${Date.now()}`;
    const fixtureRoot = join(process.cwd(), 'app', 'features', context);
    const presentationFile = join(fixtureRoot, 'presentation', 'violation.js');
    const infrastructureFile = join(
      fixtureRoot,
      'infrastructure',
      'gateway.js'
    );

    mkdirSync(join(fixtureRoot, 'presentation'), { recursive: true });
    mkdirSync(join(fixtureRoot, 'infrastructure'), { recursive: true });
    writeFileSync(
      presentationFile,
      "import { gateway } from '../infrastructure/gateway.js'; export { gateway };\n"
    );
    writeFileSync(infrastructureFile, 'export const gateway = {};\n');

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
          relative(process.cwd(), presentationFile),
        ],
        { cwd: process.cwd(), encoding: 'utf8' }
      );
      expect(result.status).toBe(1);
      expect(`${result.stdout}\n${result.stderr}`).toContain(
        'presentation-and-hooks-use-injected-infrastructure'
      );
    } finally {
      rmSync(fixtureRoot, { recursive: true, force: true });
    }
  });

  it('rejects a legacy hook that directly imports context infrastructure', () => {
    const context = `__hook_fixture_${process.pid}_${Date.now()}`;
    const fixtureRoot = join(process.cwd(), 'app', 'features', context);
    const hookFile = join(process.cwd(), 'app', 'hooks', `${context}.js`);
    const infrastructureFile = join(
      fixtureRoot,
      'infrastructure',
      'gateway.js'
    );

    mkdirSync(join(fixtureRoot, 'infrastructure'), { recursive: true });
    writeFileSync(
      hookFile,
      `import { gateway } from '../features/${context}/infrastructure/gateway.js'; export { gateway };\n`
    );
    writeFileSync(infrastructureFile, 'export const gateway = {};\n');

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
          relative(process.cwd(), hookFile),
        ],
        { cwd: process.cwd(), encoding: 'utf8' }
      );
      expect(result.status).toBe(1);
      expect(`${result.stdout}\n${result.stderr}`).toContain(
        'presentation-and-hooks-use-injected-infrastructure'
      );
    } finally {
      rmSync(hookFile, { force: true });
      rmSync(fixtureRoot, { recursive: true, force: true });
    }
  });

  it('rejects a component that directly imports context infrastructure', () => {
    const context = `__component_fixture_${process.pid}_${Date.now()}`;
    const fixtureRoot = join(process.cwd(), 'app', 'features', context);
    const componentFile = join(
      process.cwd(),
      'app',
      'components',
      `${context}.js`
    );
    const infrastructureFile = join(
      fixtureRoot,
      'infrastructure',
      'gateway.js'
    );

    mkdirSync(join(fixtureRoot, 'infrastructure'), { recursive: true });
    writeFileSync(
      componentFile,
      `import { gateway } from '../features/${context}/infrastructure/gateway.js'; export { gateway };\n`
    );
    writeFileSync(infrastructureFile, 'export const gateway = {};\n');

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
          relative(process.cwd(), componentFile),
        ],
        { cwd: process.cwd(), encoding: 'utf8' }
      );
      expect(result.status).toBe(1);
      expect(`${result.stdout}\n${result.stderr}`).toContain(
        'presentation-and-hooks-use-injected-infrastructure'
      );
    } finally {
      rmSync(componentFile, { force: true });
      rmSync(fixtureRoot, { recursive: true, force: true });
    }
  });
});
