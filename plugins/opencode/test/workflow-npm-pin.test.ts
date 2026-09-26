import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

/**
 * The npm version is not only about clearing trusted publishing's 11.5.1
 * floor. It decides what `npm pack` puts in the tarball, and npm's
 * forced-inclusion rules are exactly where majors differ — a file can ship
 * under one npm and be omitted by another on the same commit.
 *
 * So the pin lives in two files, and a half-bump is invisible without a test:
 * both workflows still parse, both still run, and the damage is a released
 * tarball that differs from the one CI verified. tincan#15 is that failure
 * happening.
 */
const root = join(import.meta.dirname, '..', '..', '..');
const workflows = ['ci.yml', 'publish.yml'] as const;

function read(name: string): string {
  return readFileSync(join(root, '.github', 'workflows', name), 'utf8');
}

function declaredPin(source: string): string | undefined {
  return source.match(/^\s*NPM_VERSION:\s*'([^']+)'/m)?.[1];
}

describe('the npm pin', () => {
  it('is declared by every workflow that runs npm', () => {
    for (const name of workflows) {
      expect(declaredPin(read(name)), `${name} declares no NPM_VERSION`).toBeTruthy();
    }
  });

  it('is the same version in both', () => {
    const [ci, publish] = workflows.map((n) => declaredPin(read(n)));
    expect(ci).toBe(publish);
  });

  // Declaring a pin and installing something else is the same bug wearing a
  // disguise, and it would read as fixed.
  it('is what each workflow actually installs', () => {
    for (const name of workflows) {
      const source = read(name);
      expect(source, `${name} installs an unpinned npm`).not.toMatch(/npm install -g npm@latest/);
      expect(source, `${name} does not install the pin it declares`).toMatch(
        /npm install -g npm@\$\{\{\s*env\.NPM_VERSION\s*\}\}/,
      );
    }
  });

  // Below this, trusted publishing refuses the OIDC exchange — and says so in
  // two unrelated-looking ways, neither mentioning the npm version.
  it('clears the trusted-publishing floor', () => {
    const pin = declaredPin(read('publish.yml'))!;
    const [major, minor, patch] = pin.split('.').map(Number);
    const floor = [11, 5, 1];
    const rank = (v: number[]) => v[0]! * 1e6 + v[1]! * 1e3 + v[2]!;
    expect(rank([major!, minor!, patch!])).toBeGreaterThanOrEqual(rank(floor));
  });

  // Installed after `npm ci`, the lockfile is resolved by one npm and packed
  // by another, which is most of what this pin exists to prevent.
  it('is installed before the dependencies it resolves', () => {
    for (const name of workflows) {
      // Comments are stripped first: both files mention `npm ci` in prose
      // explaining the ordering, and matching that instead of the step makes
      // this assertion fail on a workflow that is correct.
      const source = read(name)
        .split('\n')
        .filter((line) => !line.trim().startsWith('#'))
        .join('\n');

      const pin = source.indexOf('npm install -g npm@${{ env.NPM_VERSION }}');
      const install = source.indexOf('npm ci');
      if (install === -1) continue;
      expect(pin, `${name} pins npm after npm ci`).toBeLessThan(install);
    }
  });
});

describe('the CLI package in CI', () => {
  const ci = read('ci.yml');

  /** The job blocks, without the file's header comment — which discusses
   *  `npm pack` in prose and otherwise reads as a job that packs nothing. */
  const jobs = ci.slice(ci.indexOf('\njobs:')).split(/^  \w[\w-]*:$/m).slice(1);

  // Every check below loops over `jobs` and would pass on an empty list, so
  // the parse is asserted before anything is concluded from it.
  it('finds the jobs it means to check', () => {
    expect(jobs).toHaveLength(2);
    for (const job of jobs) expect(job).toMatch(/runs-on:/);
  });

  // A tarball whose binary is missing or unrunnable passes every static check
  // in this repository. Running it is the only thing that catches it.
  it('installs the packed tarball and runs the binary', () => {
    expect(ci).toMatch(/npm install -g --prefix/);
    expect(ci).toMatch(/bin\/birddog" --version/);
  });

  // npm pack --dry-run runs prepack, and prepack compiles Go. A job that packs
  // the CLI package without a Go toolchain fails in a way that reads like a
  // packaging bug.
  it('sets up Go in every job that packs the CLI package', () => {
    for (const job of jobs) {
      if (!/--package cli|npm pack/.test(job)) continue;
      expect(job, 'a job packs the CLI package without setup-go').toMatch(/actions\/setup-go/);
    }
  });

  // os/cpu bind the project being worked in, not only its dependencies: npm ci
  // at this root fails EBADPLATFORM on linux, under npm 10 and the pinned 12
  // alike. Every job that runs npm ci here has to be on macOS.
  it('runs npm ci only on macOS', () => {
    for (const job of jobs) {
      if (!/npm ci/.test(job)) continue;
      expect(job, 'a job runs npm ci on a platform this package refuses').toMatch(
        /runs-on: macos-latest/,
      );
    }
  });
});

describe('the release', () => {
  const publish = read('publish.yml');

  // The pipeline's smoke test runs the binary, and ubuntu cannot execute a
  // darwin Mach-O. A publish job on ubuntu would have to drop the one check
  // that catches a broken bin.
  it('publishes from macOS, with a Go toolchain', () => {
    expect(publish).not.toMatch(/runs-on: ubuntu-latest/);
    expect(publish).toMatch(/actions\/setup-go/);
  });

  it('publishes both packages from the same run', () => {
    expect(publish).toMatch(/@brutalsystems\/birddog@/);
    expect(publish).toMatch(/@brutalsystems\/birddog-opencode@/);
  });

  // v0.1.3 published fine and this workflow reported the CLI unserved for the
  // whole polling window, because `npm view` answered from the runner's
  // cached packument — including the 404s cached by checking too early, which
  // is exactly what makes a poll loop check again. The published-artifact
  // diff is gated on that answer, so it was skipped for BOTH packages on a
  // green run.
  it('reads the registry fresh, never the runner cache', () => {
    const reads = publish
      .split('\n')
      .filter((l) => /npm view |npm pack "\$name|npm pack "@/.test(l))
      .filter((l) => !l.trimStart().startsWith('#'));

    expect(reads.length, 'no registry reads found — has this workflow changed shape?').
      toBeGreaterThan(0);
    for (const line of reads) {
      expect(line, `registry read without --prefer-online:\n  ${line.trim()}`).toMatch(
        /--prefer-online/,
      );
    }
  });

  // Every retry window here waits on the same thing — a publish becoming
  // readable — so they are checked together rather than one at a time.
  //
  // Measured at v0.1.4: the plugin took 5.5 minutes to answer `npm view` and
  // another ~3.5 to become downloadable. Both windows were shorter than that.
  // The serve poll was widened first and the download loop kept its original
  // 6 x 20s, which then gave up 80 seconds before the tarball existed and
  // reported the contents unverified — a check calling something unverified
  // when it was fine, which is the failure being designed out.
  it('gives every registry wait a window wider than a publish takes', () => {
    const windows = [...publish.matchAll(/seq 1 (\d+)[\s\S]{0,800}?sleep (\d+)/g)].map(
      ([, attempts, delay]) => Number(attempts) * Number(delay),
    );

    expect(windows.length, 'no retry loops found — has this workflow changed shape?').toBe(2);
    for (const w of windows) {
      expect(w, `a registry wait is only ${w}s; a publish has taken longer`).
        toBeGreaterThanOrEqual(600);
    }
  });

  // The two exist as one version on purpose; a release that ships one of them
  // is the drift the split was meant to prevent.
  it('reads the release version from the root package', () => {
    expect(publish).toMatch(/require\('\.\/package\.json'\)\.version/);
  });
});
