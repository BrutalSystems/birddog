import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const repoRoot = join(import.meta.dirname, '..', '..', '..');

function dryRun(pkg: string): string {
  return execFileSync(
    'node',
    [join('scripts', 'claim-package-name.mjs'), '--dry-run', '--package', pkg],
    { cwd: repoRoot, encoding: 'utf8' },
  );
}

describe('claiming a package name', () => {
  it('claims the CLI name with a throwaway version', () => {
    const out = dryRun('cli');
    expect(out).toMatch(/@brutalsystems\/birddog\b/);
    expect(out).toMatch(/0\.0\.0/);
  });

  it('still claims the plugin name', () => {
    expect(dryRun('plugin')).toMatch(/@brutalsystems\/birddog-opencode/);
  });

  // The claim exists to create a name, not to ship an artifact. A 0.0.0 that
  // carried the real bin would be an unprovenanced binary on the registry.
  it('ships no binary in the claim', () => {
    expect(dryRun('cli')).not.toMatch(/bin\/birddog/);
  });
});
