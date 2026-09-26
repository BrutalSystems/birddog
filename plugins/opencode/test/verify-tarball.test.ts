import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const repoRoot = join(import.meta.dirname, '..', '..', '..');

function selfTest(args: string[]): string {
  return execFileSync('node', [join('scripts', 'verify-tarball.mjs'), '--self-test', ...args], {
    cwd: repoRoot,
    encoding: 'utf8',
  });
}

describe('the tarball guard', () => {
  // A guard nobody has watched fail is not a guard. The denylist case was
  // already watched; the required-files case is new and gets the same
  // treatment.
  it('catches strays for both packages', () => {
    const out = selfTest([]);
    expect(out).toMatch(/self-test passed/);
  });

  // The allowlist only says nothing EXTRA shipped. A tarball missing the one
  // file it exists to carry passes every check the script had before this.
  it('catches a missing required file', () => {
    const out = selfTest([]);
    expect(out).toMatch(/bin\/birddog/);
    expect(out).toMatch(/missing/i);
  });

  it('knows which package it was asked about', () => {
    expect(selfTest(['--package', 'cli'])).toMatch(/bin\/birddog/);
    expect(selfTest(['--package', 'plugin'])).toMatch(/birddog\.ts/);
  });

  // The `cpu` field is hand-pinned in package.json while the binary's
  // architecture is inherited from whatever runner built it, so the two agree
  // only by coincidence. `macos-latest` is a moving alias; if it moves again
  // the published package would claim arm64 and contain something else, and
  // npm would install it happily on the wrong machines. Nothing else in the
  // pipeline looks inside the bin.
  it('catches a binary built for an architecture package.json does not declare', () => {
    const out = selfTest(['--package', 'cli']);
    expect(out).toMatch(/built for x64, but package\.json declares cpu arm64/);
  });

  // The other direction of the same mistake: not a darwin binary at all.
  // An ELF from a Linux runner satisfies every other check in this script —
  // it is one file, at the right path, under `files`.
  it('catches a bin that is not a Mach-O binary at all', () => {
    const out = selfTest(['--package', 'cli']);
    expect(out).toMatch(/not a Mach-O binary/);
  });
});
