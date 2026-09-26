import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const pluginDir = join(import.meta.dirname, '..');
const pkg = JSON.parse(readFileSync(join(pluginDir, 'package.json'), 'utf8'));
const source = readFileSync(join(pluginDir, 'birddog.ts'), 'utf8');

describe('the loader contract', () => {
  // opencode's loader reads exports["./server"] and falls back to main. It
  // does NOT read exports["."]. A package declaring only that is fetched, its
  // package.json is read, the session runs — and the plugin never executes,
  // with nothing logged anywhere.
  it('declares the entry point the loader actually reads', () => {
    expect(pkg.exports?.['./server']).toBeTruthy();
    expect(pkg.main).toBeTruthy();
  });

  // Every exported function in the entry file is invoked as a plugin, and the
  // loader does not descend into subdirectories.
  it('exports exactly one thing', () => {
    const exported = [...source.matchAll(/^export\s+(?:const|function|async function)\s+(\w+)/gm)];
    expect(exported.map((m) => m[1])).toEqual(['Birddog']);
  });

  it('exports no default', () => {
    expect(source).not.toMatch(/^export\s+default/m);
  });
});

describe('what ships', () => {
  it('ships the entry point and the library', () => {
    expect(pkg.files).toContain('birddog.ts');
    expect(pkg.files).toContain('birddog-lib');
  });

  // The plugin is loaded untranspiled, so the tarball is the source — which
  // makes it easy to ship the wrong source. The tests sit in the same tree.
  it('ships no tests', () => {
    expect(pkg.files).not.toContain('test');
    for (const entry of pkg.files as string[]) {
      expect(entry).not.toMatch(/^\.$/);
      expect(entry).not.toMatch(/^src/);
    }
  });

  it('is published publicly under the organisation scope', () => {
    expect(pkg.name).toBe('@brutalsystems/birddog-opencode');
    expect(pkg.publishConfig?.access).toBe('public');
  });
});

describe('version agreement', () => {
  // A plugin whose version has drifted from the binary reading its records is
  // the failure muster warns about: the package updates, the loaded copy does
  // not, and nothing surfaces the difference.
  it('matches the version compiled into the entry point', () => {
    const declared = source.match(/const VERSION = '([^']+)'/)?.[1];
    expect(declared).toBe(pkg.version);
  });

  it('matches the root package version', () => {
    const root = JSON.parse(readFileSync(join(pluginDir, '..', '..', 'package.json'), 'utf8'));
    expect(pkg.version).toBe(root.version);
  });
});
