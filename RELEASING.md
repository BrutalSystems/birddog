# Releasing birddog

birddog publishes two packages from one tag, in one version:

- [`@brutalsystems/birddog`](https://www.npmjs.com/package/@brutalsystems/birddog)
  — the CLI. Its `bin` is the compiled darwin/arm64 binary, built by `prepack`
  and shipped inside the tarball, so an install needs no Go toolchain.
- [`@brutalsystems/birddog-opencode`](https://www.npmjs.com/package/@brutalsystems/birddog-opencode)
  — the opencode plugin, which has to exist on the registry for muster to
  install it by specifier at launch.

Releasing them separately would reintroduce the version skew the plugin split
exists to prevent, so one tag ships both.

The process follows [docs/ci-cd-standard.md](./docs/ci-cd-standard.md), shared
with tincan and muster. Read that for the reasoning; this is the checklist.

| Say | It means |
|---|---|
| "cut a release" | `npm version <bump> -m "%s — <what changed>"`, and nothing else. The pushed tag publishes both packages. |
| "...and update globally" | After the run finishes: `npm i -g @brutalsystems/birddog@<version>`, then check `birddog --version`. |

**The tag is what publishes.** `package.json` shows `version` and
`postversion` and says nothing about publishing, which reads like "this tags
but does not publish". It does publish — `publish.yml` fires on `v*.*.*` and
authenticates over OIDC, with no token and no separate `npm publish` step.

**Always pass `-m`.** There is no `RELEASE_NOTES.md` here; the GitHub Release
is created with `--notes-from-tag`, so the tag annotation is the public
release record. Without `-m` it ships empty.

## Choosing the version

From 1.0, ordinary semver. **Major** for a change to something a consumer can
rely on — the shape of an event or status record, the CLI surface a script
calls (commands, flags, `--json` output), or the *meaning* of a reported
state, such as what `unavailable` or `(stale)` asserts. **Minor** for a
feature that leaves every existing reader correct, new adapters and new
commands included. **Patch** for a fix.

birddog spent `0.x` under a different rule, where the *minor* carried the
breaking-change signal because there was no major to spend. Release notes from
before 1.0 are written under that rule and should be read under it. The
reasoning, and what the change costs, is
[D5](./docs/decisions.md#d5--while-0x-the-minor-is-the-breaking-change-signal).

tincan follows ordinary semver too, with address-format changes as major;
muster still documents the 0.x shape birddog has now left. The three
repositories are independent — do not carry a rule between them on the
assumption they agree.

## Release

Commit your change first, as an ordinary commit: `npm version` needs a clean
tree, which is the only reason this is two steps rather than one.

```sh
npm version patch -m "%s — <what changed>"    # or minor / major
gh run watch --repo BrutalSystems/birddog
```

That is the whole release. It rewrites all four version sites, commits them,
creates the `v<version>` tag, and pushes commit and tag — which fires
`publish.yml`.

The message becomes both the commit subject and the tag annotation, and the
tag annotation becomes the GitHub Release body. Write it as release notes.

**If you need the pieces separately** — a dry run, or a release built by hand:

```sh
npm run sync-version      # rewrites the four sites and nothing else
npm run check-version     # reports drift without changing anything
npm version <v> --no-git-tag-version   # bump without committing or tagging

make check                             # gofmt, vet, go test -race, smoke
npm run typecheck:plugin && npm test
node scripts/verify-tarball.mjs --self-test && node scripts/verify-tarball.mjs
```

Neither publishes. Only a pushed `v*` tag does.

> `postversion` pushes, and it runs even under `--no-git-tag-version`. With
> nothing new to push that is a harmless no-op, but it will carry any other
> local commits on the branch with it.

## The four version sites

Root `package.json` is the source of truth — and, since it became
`@brutalsystems/birddog`, a published artifact in its own right rather than
only a place the number lives. The `version` lifecycle hook runs
`scripts/sync-version.mjs`, which copies it into:

| Site | Why it matters |
|---|---|
| `plugins/opencode/package.json` | what the registry serves |
| `plugins/opencode/birddog.ts` | what the plugin reports at runtime |
| `internal/version/version.go` | what the binary reports |

The runtime constant is the one that catches a stale install: comparing
`grep plugin_version ~/.birddog/opencode/*.json` against the installed package
is how an operator sees a plugin that has silently lagged.

## Why the agreement is enforced twice

This is checked by `npm run check-version` in CI and by a Go test — because the failure it prevents is invisible. A plugin whose version
has drifted from the binary reading its records keeps working, keeps
publishing, and reports a version nobody can reconcile with what is installed.
muster's docs describe exactly that happening: a copied plugin silently lagged
its server for five releases.

## One-time setup, before the first release

Publishing authenticates with npm trusted publishing over OIDC. **There is no
`NPM_TOKEN` secret in this repository, and there should never be one.**

Registering the trusted publisher requires 2FA, so it cannot be scripted and
has to be done by a human with the npm account. It also **cannot be done
first**, which is the part worth knowing before you try.

### The name has to exist before it can be trusted

`npm trust` on a package the registry has never seen answers:

```
npm error code E404
npm error 404 Not Found - POST .../@brutalsystems%2fbirddog-opencode/trust - Package not found
```

So the first publish has to come from a human, and a publish by hand has no
provenance — it did not come from a workflow run, so nothing signs it.

> Whether the missing package is *strictly* the cause is **not established**.
> Both times this was hit — tincan on 2026-09-20, birddog on 2026-09-21 — the
> failing command was also still printing "Authenticate your account at: …",
> so unfinished auth may be the real cause. Publishing first works either way,
> which is why it is the order given here.

Do not hand-publish the real version. Two reasons: that version would be the
one people install, unsigned; and the publish job treats a version the
registry already serves as a green no-op, with the GitHub Release step gated
behind the same condition — so the release tag would do nothing at all.

Claim the name with a throwaway `0.0.0` instead, and let CI publish everything
anyone actually installs:

**Both of these are per package.** A trusted publisher is registered against
one name, so a tag pushed with only one registered fails at that package's
publish step — after the other has already shipped, which is not recoverable.

```sh
npm install -g npm@latest    # requires npm >= 12; on 11.x trust registration
                             # fails two ways, neither mentioning the version
npm login                    # interactive, prompts for an OTP
npm whoami                   # confirm it finished before going further

# The environment must already exist on the GitHub side.
gh api -X PUT repos/BrutalSystems/birddog/environments/npm

# 1. Claim both names. Publishes 0.0.0 from a temp staging directory; the repo
#    is untouched, and a name already claimed finishes green with nothing done.
node scripts/claim-package-name.mjs --package cli --dry-run
node scripts/claim-package-name.mjs --package cli
node scripts/claim-package-name.mjs --package plugin --dry-run
node scripts/claim-package-name.mjs --package plugin

# 2. Attach trust to each, now that the names exist.
npm trust github @brutalsystems/birddog \
  --file publish.yml \
  --repo BrutalSystems/birddog \
  --env npm \
  --allow-publish

npm trust github @brutalsystems/birddog-opencode \
  --file publish.yml \
  --repo BrutalSystems/birddog \
  --env npm \
  --allow-publish
```

The CLI claim ships a minimal 0.0.0 with no `bin` and no `files`: a name claim
needs a name, and a hand-published 0.0.0 carrying the real 7 MB binary would
put an unprovenanced artifact on the registry.

`npm trust list @brutalsystems/birddog-opencode` is the useful diagnostic: if
it answers, the credential is fine and any failure is in the request.

A 404 from the registry for a minute or two after a publish is normal and
proves nothing — `npm publish` is asynchronous and says so. `npm access list
packages @brutalsystems` updates immediately.

> **Check with `--prefer-online`, or you will read your own stale cache.**
> After v0.1.3, `npm view` and `npm install` both reported
> `@brutalsystems/birddog@0.1.3` missing for a quarter of an hour while a
> direct fetch of the packument showed it present with `latest` already
> pointing at it. The CLI had cached the packument from before the publish —
> including from the 404s of checking too early, which is the very thing that
> makes someone check again. `npm view --prefer-online` and
> `npm install --prefer-online` both answered correctly at once.

Until the trust registration exists, the publish job fails at the publish step
— which is correct. Everything before it still runs, so a tag pushed early
tells you the build is good and only the trust is missing.

Add required reviewers to the `npm` environment under **Settings →
Environments** if publishing should be narrower than "anyone who can push a
tag". OIDC widens publish rights from whoever holds a token to whoever can
push a tag, which is the trade being made.
