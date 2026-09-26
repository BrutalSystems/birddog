#!/usr/bin/env node
/**
 * Claim an npm package name with a throwaway 0.0.0, so the first real release
 * can come from CI with provenance.
 *
 * Why this exists: `npm trust` cannot register a trusted publisher for a
 * package that does not exist yet — it answers
 *
 *   npm error 404 Not Found - POST .../trust - Package not found
 *
 * and OIDC publishing therefore cannot be set up before the first publish.
 * That first publish has to come from a human, and a publish by hand has no
 * provenance: it did not come from a workflow run, so nothing signs it.
 *
 * Publishing the real version by hand would also make the release tag a green
 * no-op — the publish job skips a version the registry already serves, and
 * the GitHub Release step is gated behind the same condition. So 0.0.0 takes
 * the unprovenanced publish, and every version anyone installs comes from CI.
 *
 * The repository is left untouched: the claim is staged in a temp directory.
 *
 * The CLI claim deliberately ships NO bin and no files. A name claim needs a
 * name; a 0.0.0 carrying the real binary would put an unprovenanced 7 MB
 * artifact on the registry, which is the thing this script exists to avoid.
 *
 * Usage:
 *   node scripts/claim-package-name.mjs --package cli --dry-run
 *   node scripts/claim-package-name.mjs --package plugin
 */
import { execFileSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const dryRun = process.argv.includes('--dry-run');

const which = (() => {
  const i = process.argv.indexOf('--package');
  const key = i === -1 ? null : process.argv[i + 1];
  if (key !== 'cli' && key !== 'plugin') {
    console.error('usage: claim-package-name.mjs --package cli|plugin [--dry-run]');
    process.exit(1);
  }
  return key;
})();

const manifestPath =
  which === 'cli' ? join(root, 'package.json') : join(root, 'plugins', 'opencode', 'package.json');
const pkg = JSON.parse(readFileSync(manifestPath, 'utf8'));
console.log(`package:      ${pkg.name}`);
console.log(`real version: ${pkg.version} (published later, by CI, with provenance)`);
console.log(`claiming:     0.0.0 by hand, to create the name\n`);

// Staged in a temp directory, so nothing in the working tree changes and no
// revert is needed.
const staging = join(mkdtempSync(join(tmpdir(), 'bdclaim-')), which);
mkdirSync(staging, { recursive: true });

if (which === 'plugin') {
  // The plugin is small and loaded untranspiled, so claiming with a copy of it
  // costs nothing and keeps the claim recognisable on the registry.
  cpSync(join(root, 'plugins', 'opencode'), staging, { recursive: true });
  const manifest = readFileSync(join(staging, 'package.json'), 'utf8');
  writeFileSync(
    join(staging, 'package.json'),
    manifest.replace(/"version":\s*"[^"]*"/, '"version": "0.0.0"'),
  );
} else {
  // Not a copy of the repository: a minimal manifest with no bin, no files and
  // no os/cpu. os/cpu in particular would make the claim itself refuse to
  // install anywhere but an Apple-silicon Mac, for no gain — nothing ever
  // installs 0.0.0 on purpose.
  writeFileSync(
    join(staging, 'package.json'),
    JSON.stringify(
      {
        name: pkg.name,
        version: '0.0.0',
        description: 'Name claim. Every version anyone installs is published by CI with provenance.',
        license: pkg.license,
        repository: pkg.repository,
        publishConfig: { access: 'public' },
      },
      null,
      2,
    ) + '\n',
  );
  writeFileSync(
    join(staging, 'README.md'),
    `# ${pkg.name}\n\nPlaceholder 0.0.0, published to create the name. See ${pkg.homepage}\n`,
  );
}

// A new scoped package is restricted by default; publishConfig.access in the
// manifest is what makes it public.
if (pkg.publishConfig?.access !== 'public') {
  console.error('package.json does not set publishConfig.access to "public"');
  process.exit(1);
}

// An already-claimed name is not an error, for the same reason an
// already-published version is not one in publish.yml: a re-run should finish
// green with nothing to do. Without this the second run of a claim fails with
// "You cannot publish over the previously published versions: 0.0.0", which
// reads like something is wrong when the job is already done.
try {
  execFileSync('npm', ['view', `${pkg.name}@0.0.0`, 'version'], { stdio: 'ignore' });
  console.log(`${pkg.name}@0.0.0 is already on the registry — the name is claimed, nothing to do.`);
  console.log(`\nIf trust is not attached yet:\n
  npm trust github ${pkg.name} \\
    --file publish.yml --repo BrutalSystems/birddog --env npm --allow-publish
`);
  process.exit(0);
} catch {
  // Not published: carry on and claim it.
}

const args = ['publish', ...(dryRun ? ['--dry-run'] : [])];
console.log(`running: npm ${args.join(' ')}\n   in: ${staging}\n`);
try {
  execFileSync('npm', args, { cwd: staging, stdio: 'inherit' });
} catch {
  process.exit(1);
}

if (dryRun) {
  console.log('\ndry run — nothing was published');
  process.exit(0);
}

console.log(`
Name claimed. Next, attach trust so every later version comes from CI:

  npm trust github ${pkg.name} \\
    --file publish.yml --repo BrutalSystems/birddog --env npm --allow-publish

Then release ${pkg.version} the normal way — see RELEASING.md. A 404 from the
registry for a minute or two after a publish is normal and proves nothing;
'npm access list packages @brutalsystems' updates immediately.
`);
