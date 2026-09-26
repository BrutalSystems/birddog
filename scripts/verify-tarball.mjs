#!/usr/bin/env node
/**
 * Two independent checks over what `npm pack` would ship, because they catch
 * different mistakes. See docs/ci-cd-standard.md.
 *
 * ALLOWLIST — every packed path must sit under package.json `files`. Mostly a
 * formality, since `files` *is* an allowlist. It earns its keep on npm's
 * forced inclusions: `main` and `bin` targets ship whether or not `files`
 * lists them, so a `main` pointing outside `files` is the realistic escape.
 *
 * DENYLIST — no packed path may be a test, a dotfile, or build scaffolding.
 * This is the check with teeth. Widening `files` to "." passes the allowlist
 * by definition — the widened entry *is* the allowlist — and only a denylist
 * notices. For the plugin it matters more than most: it is loaded
 * untranspiled, so the tarball is the source, and the test suite sits in the
 * same tree.
 *
 * ARCHITECTURE — the check none of the others can make, because all three
 * reason about paths and this one reads the bytes. The CLI's `os`/`cpu` fields
 * are hand-pinned while `prepack` builds with no GOOS/GOARCH, so the binary's
 * architecture is whatever the release runner happened to be. The two agree
 * only by coincidence, and `macos-latest` is a moving alias — when it last
 * moved, the Intel default went away. A published package claiming arm64 and
 * containing an x86_64 or Linux binary would install happily on machines that
 * cannot run it, so the claim is checked against the image rather than trusted.
 *
 * REQUIRED — the check the other two cannot make. Both of them only ever ask
 * whether something EXTRA shipped; neither notices a tarball that shipped
 * nothing at all. A CLI package with no binary in it satisfies both and
 * installs a `bin` pointing at a file that is not there.
 *
 * Run with --self-test to watch the denylist, the required check and the
 * architecture check fail on purpose. A guard that has never been observed to
 * fail is not a guard.
 *
 * Usage:
 *   node scripts/verify-tarball.mjs                      # both packages
 *   node scripts/verify-tarball.mjs --package cli        # one of them
 *   node scripts/verify-tarball.mjs --self-test
 */
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');

/** The packages this repository publishes, and what each must contain. */
const PACKAGES = {
  cli: {
    name: '@brutalsystems/birddog',
    dir: root,
    required: ['bin/birddog', 'package.json'],
    // The packed bin, whose architecture must match the declared `cpu`. The
    // plugin package has none: it ships TypeScript and runs anywhere.
    binary: 'bin/birddog',
  },
  plugin: {
    name: '@brutalsystems/birddog-opencode',
    dir: join(root, 'plugins', 'opencode'),
    required: ['birddog.ts', 'package.json', 'LICENSE'],
  },
};

/** Paths deliberately shipped out of a denied tree go here by name, so an
 *  exception appears in review instead of hiding inside a loosened pattern.
 *  There are none. */
const EXCEPTIONS = new Set();

const DENIED = [
  { pattern: /^test(s)?\//, why: 'tests' },
  { pattern: /^src\//, why: 'untranspiled build input' },
  { pattern: /^node_modules\//, why: 'dependencies' },
  { pattern: /^\.github\//, why: 'CI configuration' },
  { pattern: /(^|\/)\.[^/]+$/, why: 'a dotfile' },
  { pattern: /\.tsbuildinfo$/, why: 'build scaffolding' },
  { pattern: /(^|\/)SPEC\.md$/, why: 'internal specification' },
];

/** `npm pack --dry-run` runs prepack, and the CLI package's prepack compiles
 *  Go. A runner without a Go toolchain can verify the plugin and nothing else,
 *  which is why this takes a selector rather than always doing both. */
function selected(argv) {
  const i = argv.indexOf('--package');
  if (i === -1) return Object.keys(PACKAGES);
  const key = argv[i + 1];
  if (!PACKAGES[key]) {
    console.error(
      `unknown package ${key ?? '(none)'} — expected one of: ${Object.keys(PACKAGES).join(', ')}`,
    );
    process.exit(1);
  }
  return [key];
}

/** packedFiles asks npm what it would ship.
 *
 *  npm 12 changed `npm pack --json` from an array to an object keyed by
 *  package name. The publish job upgrades npm, so this script runs on a
 *  toolchain no CI run has exercised — both shapes are handled deliberately.
 */
function packedFiles(dir) {
  const raw = execFileSync('npm', ['pack', '--dry-run', '--json'], {
    cwd: dir,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'ignore'],
  });
  const parsed = JSON.parse(raw);
  const entry = Array.isArray(parsed) ? parsed[0] : Object.values(parsed)[0];
  if (!entry?.files) throw new Error('npm pack --json returned no file list');
  return entry.files.map((f) => f.path);
}

/** npm ships these whether or not `files` lists them. The plugin package
 *  happens to list LICENSE itself, which is why this list was one entry long
 *  and looked sufficient until a second package stopped listing them. */
const ALWAYS = ['package.json', 'README.md', 'LICENSE', 'LICENCE', 'CHANGELOG.md'];

function readPackage(dir) {
  return JSON.parse(
    execFileSync('node', ['-p', 'JSON.stringify(require("./package.json"))'], {
      cwd: dir,
      encoding: 'utf8',
    }),
  );
}

function allowedPrefixes(pkg) {
  return [...(pkg.files ?? []), ...ALWAYS];
}

/** Mach-O cpu types, named as npm's `cpu` field names them. Only the two that
 *  could plausibly come off a macOS runner; anything else reads as unknown,
 *  which is reported rather than waved through. */
const MACHO_CPU = new Map([
  [0x0100000c, 'arm64'],
  [0x01000007, 'x64'],
]);

/** 64-bit Mach-O, little-endian — the only shape a modern darwin build takes.
 *  A fat/universal archive carries 0xcafebabe instead and is not one image, so
 *  it does not match and is reported as not being a Mach-O binary. That is the
 *  honest answer while `cpu` claims a single architecture. */
const MACHO_MAGIC_64 = 0xfeedfacf;

/** machoArch reads the architecture out of a Mach-O header.
 *
 *  Reading the bytes rather than shelling out to `file`: the header layout is
 *  fixed and the wording of `file` is not, and a guard that fails when a tool's
 *  output is reworded is a guard that will be deleted.
 *
 *  Returns null for anything that is not a 64-bit Mach-O image — an ELF from a
 *  Linux runner, a fat archive, a shell script, a truncated file. */
function machoArch(head) {
  if (head.length < 8) return null;
  if (head.readUInt32LE(0) !== MACHO_MAGIC_64) return null;
  return MACHO_CPU.get(head.readUInt32LE(4)) ?? null;
}

/** archProblems compares what was built against what package.json claims.
 *
 *  Pure over the header bytes so the self-test can hand it images no runner
 *  here would produce. Negated `cpu` entries (`!x64`) are not used by either
 *  package and are not interpreted: an architecture is acceptable only when it
 *  is named outright. */
function archProblems(path, head, cpu) {
  const arch = machoArch(head);
  if (arch === null) {
    return [`${path}: not a Mach-O binary — it cannot run on the platform package.json declares`];
  }
  if (!cpu.includes(arch)) {
    return [`${path}: built for ${arch}, but package.json declares cpu ${cpu.join(', ')}`];
  }
  return [];
}

/** binaryProblems reads the packed binary off disk.
 *
 *  Called after packedFiles, which runs prepack — so the file on disk is the
 *  one that would ship. Only the header is needed. */
function binaryProblems(dir, binary, cpu) {
  if (!binary) return [];
  if (!cpu?.length) return [`${binary}: package.json declares no cpu to check the binary against`];
  let head;
  try {
    head = readFileSync(join(dir, binary)).subarray(0, 8);
  } catch (e) {
    return [`${binary}: cannot be read (${e.code ?? e.message})`];
  }
  return archProblems(binary, head, cpu);
}

function check(files, allowed, required) {
  const problems = required
    .filter((r) => !files.includes(r))
    .map((r) => `${r}: required, but missing from the tarball`);

  for (const path of files) {
    if (EXCEPTIONS.has(path)) continue;

    const permitted = allowed.some((entry) => path === entry || path.startsWith(`${entry}/`));
    if (!permitted) {
      problems.push(`${path}: not under package.json "files"`);
    }
    for (const { pattern, why } of DENIED) {
      if (pattern.test(path)) problems.push(`${path}: ${why} must not ship`);
    }
  }
  return problems;
}

if (process.argv.includes('--self-test')) {
  // The negative cases, watched rather than assumed. An allowlist-only version
  // of this script had a negative test that never fired: removing an entry
  // from `files` just stops packing the file, so there is no stray to find.
  let failures = 0;

  /** A Mach-O header for one cpu type, which is all the check reads. */
  function machoHeader(cputype) {
    const b = Buffer.alloc(8);
    b.writeUInt32LE(MACHO_MAGIC_64, 0);
    b.writeUInt32LE(cputype, 4);
    return b;
  }

  for (const key of selected(process.argv)) {
    const { name, required, binary } = PACKAGES[key];

    const strays = ['test/state.test.ts', '.npmrc', 'src/thing.ts', 'SPEC.md'];
    const strayProblems = check(strays, ['test', '.npmrc', 'src', 'SPEC.md', 'package.json'], []);
    if (strayProblems.length !== strays.length) {
      console.error(`${name}: the denylist did not catch every stray`);
      failures++;
    }

    // The tarball that ships nothing it was supposed to.
    const empty = check([], [], required);
    for (const r of required) {
      if (!empty.some((p) => p.startsWith(`${r}:`))) {
        console.error(`${name}: a tarball missing ${r} was not reported`);
        failures++;
      }
    }

    // The wrong binary, in both the shapes a moved runner alias could produce:
    // the right platform and the wrong chip, and not a darwin binary at all.
    const archBad = binary
      ? [
          ...archProblems(binary, machoHeader(0x01000007), ['arm64']),
          ...archProblems(binary, Buffer.from('\x7fELF\x02\x01\x01\x00', 'latin1'), ['arm64']),
        ]
      : [];
    if (binary && archBad.length !== 2) {
      console.error(`${name}: the architecture check did not catch every wrong binary`);
      failures++;
    }

    // And the direction that must stay quiet. A check that fires on the
    // correct binary would fail every release and be removed within a day.
    if (binary && archProblems(binary, machoHeader(0x0100000c), ['arm64']).length !== 0) {
      console.error(`${name}: the architecture check rejected the binary it should accept`);
      failures++;
    }

    console.log(
      `${name}: caught ${strayProblems.length} strays, ${empty.length} missing required, ${archBad.length} wrong binaries`,
    );
    for (const p of [...strayProblems, ...empty, ...archBad]) console.log(`  ${p}`);
  }

  if (failures > 0) {
    console.error('\nself-test FAILED');
    process.exit(1);
  }
  console.log('\nself-test passed');
  process.exit(0);
}

let failed = false;
for (const key of selected(process.argv)) {
  const { name, dir, required, binary } = PACKAGES[key];
  const pkg = readPackage(dir);
  const files = packedFiles(dir);
  // After packedFiles, which runs prepack: the built binary is now on disk.
  const problems = [
    ...check(files, allowedPrefixes(pkg), required),
    ...binaryProblems(dir, binary, pkg.cpu),
  ];

  console.log(`\n${name} would ship ${files.length} files:`);
  for (const f of [...files].sort()) console.log(`  ${f}`);

  if (problems.length > 0) {
    console.error(`\n${name}: tarball verification failed:`);
    for (const p of problems) console.error(`  ${p}`);
    failed = true;
  } else {
    console.log(`\n${name}: tarball verified`);
  }
}
if (failed) process.exit(1);
