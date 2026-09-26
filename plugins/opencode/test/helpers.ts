import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

/**
 * unwritableDir returns a path that cannot be a directory on any POSIX
 * system: its parent is a regular file, so mkdir fails with ENOTDIR
 * immediately.
 *
 * A hardcoded path like /proc/nonexistent is not portable — it fails fast on
 * macOS and hung the Linux runner for the full test timeout, seven times over.
 */
export function unwritableDir(): string {
  const base = mkdtempSync(join(tmpdir(), 'bdunwritable-'));
  const file = join(base, 'a-file');
  writeFileSync(file, 'not a directory');
  return join(file, 'birddog');
}
