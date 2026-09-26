import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

/**
 * The shared standard says its copies must read the same, and nothing checks
 * it. Mike has ruled the three repositories stay independent — no CI step
 * comparing them, no shared file the others link to — so nothing can check
 * that claim across repos without reintroducing the coupling he vetoed.
 *
 * What a repository CAN check is its own copy, and mechanical drift is what
 * actually happened three times this week: two-repository wording surviving a
 * copy into a third repo, a paragraph duplicated by a re-paste, and a local
 * deviation written inline where it reads as shared standard.
 *
 * This catches that class. It cannot catch two copies disagreeing about
 * meaning — nothing self-contained can.
 */
const repoRoot = join(import.meta.dirname, '..', '..', '..');
const standard = readFileSync(join(repoRoot, 'docs', 'ci-cd-standard.md'), 'utf8');

/** Body text of each `##` section, keyed by heading. */
function splitSections(md: string): Map<string, string> {
  const out = new Map<string, string>();
  let heading = '';
  for (const line of md.split('\n')) {
    const m = /^## (.+)$/.exec(line);
    if (m?.[1]) {
      heading = m[1];
      out.set(heading, '');
    } else if (heading) {
      out.set(heading, (out.get(heading) ?? '') + line + '\n');
    }
  }
  return out;
}

describe('the shared CI/CD standard', () => {
  // Was a list of three literal dead phrases — "either repository", "Both
  // repositories" — each already deleted and none able to return in that
  // form. A check tuned to the corpse. Generalised to any counting word
  // qualifying the repository noun, so wording nobody has written yet is
  // caught too.
  //
  // "Two workflows per repository" must stay legal: there the count qualifies
  // the workflows, not the repositories, which is why this matches the noun
  // directly rather than counting words anywhere near it.
  it('carries no wording from when there were only two repositories', () => {
    const stale = standard.match(/\b(both|either|two|the other) (repo|repos|repository|repositories)\b/gi) ?? [];
    expect(stale, 'wording from when there were only two repositories').toEqual([]);
  });

  // Mike ruled the three repositories independent. A copy claiming they say
  // the same thing contradicts that ruling and cannot be true of anything
  // nobody checks — this file made that claim in its own header for a day
  // after I reported it removed.
  //
  // Both halves matter. Banning the claim alone would make deleting the
  // paragraph the cheapest way to pass, so the statement that replaces it is
  // asserted present.
  it('describes the copies as independent rather than identical', () => {
    const claims = standard
      .split('\n')
      .filter((line) => /say the same thing|identical|kept in sync|must match the other/i.test(line))
      // The file may legitimately DENY sameness; it may not assert it.
      .filter((line) => !/\bnot\b|\bno\b|never|rather than/i.test(line));

    expect(claims, 'a standing claim that the copies agree').toEqual([]);

    // Scoped to the opening note, and demanding both ideas. Searching the
    // whole document for "independent" passed while the note was deleted,
    // because the verify-tarball section calls its checks independent — a
    // positive half satisfied by an unrelated sentence guards nothing.
    const note = standard.slice(0, standard.indexOf('\nTwo workflows'));
    expect(
      /independent/i.test(note) && /drift|diverge/i.test(note),
      'the opening note no longer says the copies are independent and will drift',
    ).toBe(true);
  });

  // A local fact written as ordinary prose is indistinguishable from shared
  // standard, which is the whole failure the marked form exists to prevent.
  it('marks every repository-specific claim rather than stating it inline', () => {
    const sentences = standard
      .split('\n')
      .filter((line) => /\bbirddog\b/i.test(line))
      // The opening paragraph names all three repositories legitimately.
      .filter((line) => !line.includes('github.com/BrutalSystems/birddog'))
      .filter((line) => !line.includes('Copied into birddog'));

    for (const line of sentences) {
      const marked =
        line.includes('Repository-specific, birddog') || line.trimStart().startsWith('>');
      expect(marked, `unmarked repository-specific claim:\n  ${line.trim()}`).toBe(true);
    }
  });

  // Leading whitespace is allowed: a blockquote inside a numbered list has to
  // be indented, and one of these notes qualifies a step in Releasing.
  it('opens every repository-specific note the same way', () => {
    const notes = standard.match(/^\s*> \*\*Repository-specific, \w+\.\*\*/gm) ?? [];
    const mentions = standard.match(/Repository-specific/g) ?? [];
    expect(notes.length, 'a note is not in the agreed form').toBe(mentions.length);
  });

  // tincan carried the same paragraph twice from 0.5.1 until 3678b39, added by
  // re-pasting a paragraph to append one sentence to the copy.
  it('repeats no paragraph', () => {
    const paragraphs = standard
      .split(/\n\s*\n/)
      .map((p) => p.trim())
      .filter((p) => p.length > 120 && !p.startsWith('|') && !p.startsWith('```'));

    const seen = new Set<string>();
    for (const p of paragraphs) {
      expect(seen.has(p), `duplicated paragraph:\n  ${p.slice(0, 90)}…`).toBe(false);
      seen.add(p);
    }
  });

  // Agreed shape: the fork guard is its own section with its reason recorded,
  // and the sentence is DELETED from Pipeline order rather than left in both.
  // Leaving it in both is how a rule acquires two copies that drift apart.
  it('states the fork guard once, in its own section', () => {
    const mentioning = [...splitSections(standard)]
      .filter(([, body]) => /fork guard|github\.repository ==/i.test(body))
      .map(([heading]) => heading);

    expect(mentioning, 'the fork guard rule is stated outside its own section').toEqual([
      'Fork guard',
    ]);
  });

  // The standard describes this repository's own pipeline, so a claim it
  // makes about these workflows is checkable against them. This caught the
  // callout that survived the pin: it still said the publish job installs
  // npm@latest, contradicting the floor section two headings above it.
  //
  // Written as an allowlist rather than a search for the sentence already
  // deleted, because that sentence will never appear again and a check that
  // can only catch the corpse guards nothing. A new or reworded mention has
  // to be justified here, which is the point — the last one went false when
  // the WORKFLOWS changed, not when the prose did.
  it('makes no claim about this repository\'s workflows that the workflows contradict', () => {
    for (const workflow of ['ci.yml', 'publish.yml']) {
      const yaml = readFileSync(join(repoRoot, '.github', 'workflows', workflow), 'utf8');
      expect(
        /npm install -g npm@\$\{\{ env\.NPM_VERSION \}\}/.test(yaml),
        `precondition: ${workflow} pins npm`,
      ).toBe(true);
      expect(/npm install -g npm@latest/.test(yaml), `${workflow} installs a floating npm`).toBe(
        false,
      );
    }

    const justified = [
      // The rule itself.
      /do not use `npm@latest`/,
      // A hypothetical repository, not this one.
      /a repository running `npm@latest`/,
      // Past tense: what happened before the pin, and why the pin exists.
      /installed `npm@latest` and CI ran/,
      // The one-time `npm trust` step, run on a person's own machine, which
      // deliberately wants a current npm rather than the runner's pin.
      /^npm install -g npm@latest$/,
    ];

    const unjustified = standard
      .split('\n')
      .filter((line) => line.includes('npm@latest'))
      .filter((line) => !justified.some((ok) => ok.test(line.trim().replace(/^>\s*/, ''))));

    expect(unjustified, 'unjustified npm@latest claim — does it describe these workflows?').toEqual(
      [],
    );

    // Without this, deleting the rule is the cheapest way to satisfy the
    // allowlist above — a guard whose easiest fix is removing what it guards.
    expect(
      /do not use `npm@latest`/.test(standard),
      'the rule the allowlist exists to protect is gone',
    ).toBe(true);
  });

  it('keeps the agreed section set', () => {
    const headings = [...standard.matchAll(/^(#{2,3}) (.+)$/gm)].map((m) => m[2]);
    for (const required of [
      'Publishing uses OIDC, never a token',
      'npm version floor',
      'Fork guard',
      'Pipeline order',
      '`scripts/verify-tarball.mjs`',
      'One-time setup, per package',
      'Releasing',
    ]) {
      expect(headings, `missing section: ${required}`).toContain(required);
    }
  });
});
