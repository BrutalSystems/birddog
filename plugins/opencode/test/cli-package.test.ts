import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const repoRoot = join(import.meta.dirname, '..', '..', '..');
const pkg = JSON.parse(readFileSync(join(repoRoot, 'package.json'), 'utf8'));

describe('the birddog CLI package', () => {
  it('is published publicly under the organisation scope', () => {
    expect(pkg.name).toBe('@brutalsystems/birddog');
    expect(pkg.private).toBeUndefined();
    expect(pkg.publishConfig?.access).toBe('public');
  });

  // The bin target is the compiled binary itself. npm links and chmods bin
  // targets, so there is nothing for a JS shim to do until a second
  // architecture exists to dispatch on.
  it('ships the binary as its bin target', () => {
    expect(pkg.bin).toEqual({ birddog: './bin/birddog' });
    expect(pkg.files).toContain('bin/birddog');
  });

  // internal/platform/* has no non-darwin files at all, so a linux install
  // could only produce a binary that cannot exist. Let npm refuse it with
  // EBADPLATFORM rather than plant something unrunnable.
  it('refuses platforms the binary cannot exist on', () => {
    expect(pkg.os).toEqual(['darwin']);
    expect(pkg.cpu).toEqual(['arm64']);
  });

  // /bin/ is gitignored: the binary is a build output, made at pack time. A
  // clone without Go must fail loudly at pack rather than publish an empty bin.
  it('builds the binary at pack time', () => {
    expect(pkg.scripts.prepack).toMatch(/go build/);
    expect(pkg.scripts.prepack).toContain('./cmd/birddog');
  });
});
