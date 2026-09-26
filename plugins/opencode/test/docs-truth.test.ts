import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const repoRoot = join(import.meta.dirname, '..', '..', '..');
const read = (...p: string[]) => readFileSync(join(repoRoot, ...p), 'utf8');

describe('what the docs claim about publishing', () => {
  // These sentences were true until the CLI package existed. A reader who
  // believes them goes looking for a release process that is no longer there.
  it('no longer says birddog publishes nothing', () => {
    for (const file of ['RELEASING.md', join('docs', 'ci-cd-standard.md')]) {
      expect(read(file), `${file} still says birddog publishes no package`).not.toMatch(
        /publishes no npm package of its own/,
      );
    }
  });

  it('tells a reader how to install the command', () => {
    expect(read('README.md')).toMatch(/npm i -g @brutalsystems\/birddog/);
  });

  // The Go route stays: it is how anyone without npm, or working from a
  // clone, gets the binary.
  it('keeps the source route', () => {
    expect(read('README.md')).toMatch(/go (build|install)/);
  });

  // A session that reads package.json alone concludes "this tags but does not
  // publish" — the scripts show `version` and `postversion` and hide the rest.
  // That conclusion has been reported once already, while the publish it
  // described was running, so the correction is written in both files a
  // session reads — and checked here, because two copies of a claim are two
  // things that can drift.
  it('says in both places that the tag is what publishes', () => {
    for (const file of ['CLAUDE.md', 'RELEASING.md']) {
      expect(read(file), `${file} does not say the tag publishes`).toMatch(
        /tag (is what|that) publishes/i,
      );
    }
  });

  // The GitHub Release is created with --notes-from-tag, so a cut without -m
  // ships a release with an empty body. That is only true while there is no
  // RELEASE_NOTES.md — if one is ever added, this warning has to go.
  it('warns about -m only while the tag annotation is the release record', () => {
    const publish = read(join('.github', 'workflows', 'publish.yml'));
    if (!/--notes-from-tag/.test(publish)) return;
    expect(read('CLAUDE.md')).toMatch(/-m\b/);
    expect(read('RELEASING.md')).toMatch(/Always pass `-m`/);
  });

  // The global install is the CLI's name, not the plugin's: opencode fetches
  // the plugin by specifier and nobody installs it by hand.
  it('names the package a session should install globally', () => {
    expect(read('CLAUDE.md')).toMatch(/npm i -g @brutalsystems\/birddog@/);
  });

  // The bump rule lives in three files by design — the decision, the checklist
  // and the note a session reads — so all three can disagree. D5 is the one
  // that reasons; the others must not contradict it.
  it('states the same bump rule everywhere it states one', () => {
    // birddog left the 0.x rule at 1.0. D5 is kept rather than deleted,
    // because releases before 1.0 were made under it and have to be readable
    // under it — so the entry must survive AND say it no longer governs.
    const d5 = read(join('docs', 'decisions.md'));
    expect(d5).toMatch(/## D5 — while 0\.x, the minor is/);
    expect(d5, 'D5 does not say it was superseded at 1.0').toMatch(/Superseded at 1\.0/);

    // Every place that states the rule must state the current one. Three
    // files said it before and drifted independently; that is what this
    // guards.
    for (const file of ['RELEASING.md', 'CLAUDE.md', 'README.md']) {
      expect(read(file), `${file} does not carry the 1.0 bump rule`).toMatch(
        /ordinary semver|semver from 1\.0/i,
      );
      expect(read(file), `${file} does not say what earns a major`).toMatch(
        /major/i,
      );
    }

    // The repositories are independent. A copy that starts citing a sibling
    // as authority is how a house style gets invented.
    expect(read('RELEASING.md')).toMatch(/repositories are independent/);
  });

  // D4 decided this repository is public and that machine-specific detail
  // stays out of it. A home path or an address is the shape that leaks, and a
  // first scan for this missed real hits because of a bad pathspec — so it is
  // a test now rather than a thing anyone remembers to grep for.
  // The list covers what a developer writes while looking at their own
  // machine. docs/troubleshooting.md is written from a symptom someone just
  // reproduced locally, and scripts/replay-activity.mjs reads that machine's
  // own instance stores — both are the shape that leaks.
  it('carries no home paths or addresses', () => {
    const files = [
      'README.md',
      'RELEASING.md',
      'CLAUDE.md',
      join('docs', 'decisions.md'),
      join('docs', 'ci-cd-standard.md'),
      join('docs', 'troubleshooting.md'),
      join('scripts', 'replay-activity.mjs'),
    ];
    for (const file of files) {
      const text = read(file);
      expect(text, `${file} names a home directory`).not.toMatch(/\/Users\/[a-z]/i);
      expect(text, `${file} carries an address`).not.toMatch(/[\w.]+@[\w.]+\.\w{2,}/);
    }
  });

  // Both names need their own claim and their own trusted publisher; trust is
  // per package. A release tag with only one registered fails at publish.
  it('records the per-package setup both names need', () => {
    const releasing = read('RELEASING.md');
    expect(releasing).toMatch(/--package cli/);
    expect(releasing).toMatch(/--package plugin/);
    expect(releasing).toMatch(/npm trust github @brutalsystems\/birddog\b/);
  });
});
