#!/usr/bin/env node
/**
 * One version, four places.
 *
 *   package.json                          (source of truth; npm bumps it)
 *   plugins/opencode/package.json         what the registry serves
 *   plugins/opencode/birddog.ts           what the plugin reports at runtime
 *   internal/version/version.go           what the binary reports
 *
 * The root package.json is the source of truth. This copies it into the
 * published plugin package and into the Go build, and `--check` fails when
 * they disagree — so a half-bump fails the build instead of shipping a plugin
 * whose version does not match the binary that reads it.
 *
 * A plugin that silently lags is the exact failure muster warns about, and
 * version skew is how it happens.
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const check = process.argv.includes('--check');

const source = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')).version;
if (!source) {
  console.error('root package.json has no version');
  process.exit(1);
}

let failed = false;

/** json updates (or checks) a version field in a JSON file. */
function json(relative) {
  const path = join(root, relative);
  const raw = readFileSync(path, 'utf8');
  const parsed = JSON.parse(raw);
  if (parsed.version === source) return;

  if (check) {
    console.error(`${relative}: ${parsed.version} != ${source}`);
    failed = true;
    return;
  }
  // Rewrite the line rather than re-serialising, to leave formatting alone.
  writeFileSync(path, raw.replace(/"version":\s*"[^"]*"/, `"version": "${source}"`));
  console.log(`${relative}: -> ${source}`);
}

/** constant updates (or checks) a quoted version constant in a source file. */
function constant(relative, pattern, rewrite) {
  const path = join(root, relative);
  const raw = readFileSync(path, 'utf8');
  const found = raw.match(pattern);
  if (!found) {
    console.error(`${relative}: no version constant found`);
    failed = true;
    return;
  }
  if (found[1] === source) return;

  if (check) {
    console.error(`${relative}: ${found[1]} != ${source}`);
    failed = true;
    return;
  }
  writeFileSync(path, raw.replace(pattern, rewrite(source)));
  console.log(`${relative}: -> ${source}`);
}

/** go updates (or checks) the version constant in the Go build. */
function go(relative) {
  const path = join(root, relative);
  const raw = readFileSync(path, 'utf8');
  const found = raw.match(/const Version = "([^"]*)"/);
  if (!found) {
    console.error(`${relative}: no version constant found`);
    failed = true;
    return;
  }
  if (found[1] === source) return;

  if (check) {
    console.error(`${relative}: ${found[1]} != ${source}`);
    failed = true;
    return;
  }
  writeFileSync(path, raw.replace(/const Version = "[^"]*"/, `const Version = "${source}"`));
  console.log(`${relative}: -> ${source}`);
}

json('plugins/opencode/package.json');
go('internal/version/version.go');
// The plugin reports this at runtime, and it is how an operator tells a
// loaded plugin from the package that was installed. Left unsynced it goes
// stale silently, which is the whole failure being guarded against.
constant(
  'plugins/opencode/birddog.ts',
  /const VERSION = '([^']*)'/,
  (v) => `const VERSION = '${v}'`,
);

if (failed) {
  console.error(`\nversions disagree with root package.json (${source}); run: npm run sync-version`);
  process.exit(1);
}
if (check) console.log(`all versions agree: ${source}`);
