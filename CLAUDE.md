# birddog — working notes for Claude sessions

## Vocabulary — these are instructions, not topics to discuss

**"cut" / "cut a release"** — do the whole release, publish included. One
command:

```sh
npm version <patch|minor|major> -m "%s — <what changed>"
```

That is all of it. `npm version` rewrites the four version sites, commits,
tags `v<version>`, and `postversion` pushes commit and tag — and **the pushed
tag is what publishes**. `publish.yml` fires on `v*.*.*` and publishes both
packages over OIDC. There is no separate `npm publish` step and no npm token
anywhere in this repository.

Do not report a release as "tagged but not published". Reading `package.json`
invites exactly that mistake — its `version` and `postversion` scripts are
visible and say nothing about publishing. It is wrong: it has been reported
that way once already, while the publish it described was running.

**"...and update globally"** — after the publish run **completes**, install the
version just shipped on this machine and verify it:

```sh
npm i -g @brutalsystems/birddog@<version>
birddog --version          # must print: birddog <version>
```

Report what it printed. Install only once the run has finished: the registry
lags a publish, and an install that races it fetches the previous version.
Only the CLI is installed globally — the opencode plugin is fetched by
opencode from its own specifier and is never installed by hand.

## Always pass `-m`

birddog has no `RELEASE_NOTES.md`. The GitHub Release is created with
`--notes-from-tag`, so the tag annotation **is** the public release record. A
cut without `-m` leaves a release whose title is the version number and whose
body is empty. Write the message as release notes.

## Which bump

Ordinary semver from 1.0:

- **major** — a change to something a consumer relies on: the shape of an
  event or status record, the CLI surface a script calls, or the *meaning* of
  a reported state, such as what `unavailable` or `(stale)` asserts.
- **minor** — a feature that leaves every existing reader correct. New
  adapters and new commands are minors now.
- **patch** — a fix.

Before 1.0 the *minor* carried the breaking-change signal, because there was
no major to spend, and new adapters were patches. Release notes from then are
written under that rule and should be read under it. What counts as
*breaking* has not changed; only which number carries it.

Pick it yourself from what changed and say in one line which you picked, so a
wrong call is visible before the tag goes out. Full rule and reasoning:
`RELEASING.md`, "Choosing the version", and `docs/decisions.md` D5.

Do not import tincan's rule or muster's. The repositories are independent;
muster still documents the 0.x shape birddog has left.

## Two packages, one version

`@brutalsystems/birddog` (the CLI, whose `bin` is the compiled darwin/arm64
binary, built by `prepack` and shipped inside the tarball) and
`@brutalsystems/birddog-opencode` (the opencode plugin) ship from the same tag
in the same version. Releasing them apart reintroduces the skew the split
exists to prevent. Full detail in `RELEASING.md`.

## Before every commit

`make check` — exactly what CI runs: gofmt, `go vet`, `go test ./... -race`,
the plugin typecheck and the vitest suite.
