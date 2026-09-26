import { mkdtempSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { Birddog } from '../birddog.js';

/** Must match MAX_LOG_BYTES in birddog.ts. */
const MAX_LOG_BYTES = 2 * 1024 * 1024;

const saved = process.env.BIRDDOG_HOME;
afterEach(() => {
  if (saved === undefined) delete process.env.BIRDDOG_HOME;
  else process.env.BIRDDOG_HOME = saved;
});

/**
 * Seeds an over-bound log and returns where it lives. Writing the 2 MiB is
 * cheaper than logging our way up to it, and it exercises the case that
 * matters: the bound is crossed by a log that was already this big when the
 * process started, which is every session after the first.
 */
function seedOverBoundLog(): string {
  const home = mkdtempSync(join(tmpdir(), 'bdlog-'));
  process.env.BIRDDOG_HOME = home;
  const logPath = join(home, 'opencode-plugin.log');
  writeFileSync(logPath, 'x'.repeat(MAX_LOG_BYTES), { mode: 0o600 });
  return logPath;
}

describe('the plugin log bound', () => {
  it('truncates the log instead of appending past the bound', async () => {
    const logPath = seedOverBoundLog();

    await Birddog();

    // Size alone would also be satisfied by an overwrite in place. The old
    // contents have to be gone: discarding them is what truncation means.
    const after = readFileSync(logPath, 'utf8');
    expect(after).not.toContain('x'.repeat(1024));
    expect(after).toContain('[birddog]');
    expect(statSync(logPath).size).toBeLessThan(MAX_LOG_BYTES);
  });

  // The truncation above must not become the steady state: a sink that always
  // wrote rather than appended would also satisfy the bound, by keeping only
  // the most recent line. Diagnostics are only worth writing if they survive
  // the next line, and the next session.
  it('keeps what it already wrote while under the bound', async () => {
    const home = mkdtempSync(join(tmpdir(), 'bdlog-'));
    process.env.BIRDDOG_HOME = home;
    const logPath = join(home, 'opencode-plugin.log');

    await Birddog();
    const afterFirst = statSync(logPath).size;
    await Birddog();

    expect(afterFirst).toBeGreaterThan(0);
    expect(statSync(logPath).size).toBeGreaterThan(afterFirst);
  });
});
