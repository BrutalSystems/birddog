# birddog — documentation

A local, instance-based session monitor for coding agents on one machine.
**birddog monitors sessions; it does not manage their work.**

birddog runs: it observes Claude Code sessions, Codex threads and instrumented
opencode sessions, reports what deserves attention, and can deliver alerts to a
Claude Code orchestrator. These documents record what it does and why.

## Read in this order

| Document | What it is | Status |
|---|---|---|
| [handoff.md](./handoff.md) | The original specification — scope, boundaries, state model, CLI, acceptance criteria | Rev 1, **superseded in part** |
| [providers.md](./providers.md) | What each runtime exposes to an outside observer, and what it does not | Rev 1, **authoritative** |
| [review.md](./review.md) | What the findings mean for the plan | Rev 1 |
| [decisions.md](./decisions.md) | What was decided in response, and what it costs | Rev 1, **binding** |
| [cli.md](./cli.md) | Every command, its output, and the error codes | |
| [architecture.md](./architecture.md) | How the pieces fit, and the decisions that shaped them | |
| [troubleshooting.md](./troubleshooting.md) | What the surprising answers mean | |
| [acceptance.md](./acceptance.md) | Every acceptance criterion, and what is not done | |
| [ci-cd-standard.md](./ci-cd-standard.md) | How BrutalSystems npm packages are built and published | shared with tincan and muster |

`providers.md` overrides `handoff.md` wherever they disagree, and
`decisions.md` overrides both.

The raw probe record behind `providers.md` — installed versions, sample
records, the commands each claim was verified with — is kept out of this
repository, as muster does with its own. The conclusions are public; the
machine they were drawn from is not.

## Running it

```sh
go build -o birddog ./cmd/birddog
./birddog discover                       # find a session to watch
./birddog start --config birddog.json    # start watching it
./birddog status --instance <id>
./birddog stop --instance <id>           # watched sessions keep running
```

Sample configurations are in [`examples/`](../examples/), and
[`scripts/smoke.sh`](../scripts/smoke.sh) drives fake workers through every
state and checks what birddog reports about each.

## Where things stand

Three sections of the handoff rest on assumptions that local inspection
contradicts — **Codex**, **OpenCode** and **Orchestrator notifications**. One
section is cheaper and more capable than assumed — **Claude Code**, where a
live session registry supplies attachment, state and a PID-reuse guard with no
hooks and no restart.

Those three sections need rewriting into a build spec. Phase 1 (instance
lifecycle, storage, cursor, policy, generic process/file observation) is
unaffected by all of it and can proceed as written.

## Decisions made

Full reasoning and costs in [decisions.md](./decisions.md).

| | Decision |
|---|---|
| **D1** | Go. Discovery implemented in birddog, independent of tincan. |
| **D2** | opencode is in scope, via a plugin muster installs from npm at launch. |
| **D3** | Desktop apps out of scope for v1. |
| **D4** | Public repo under BrutalSystems, MIT. |
| **D5** | While 0.x, the minor is the breaking-change signal. **Superseded at 1.0** — ordinary semver since. |
| **D6** | `no_progress` ships with no default: the signal it thresholds is not comparable across providers. |
| **D7** | Event retention is opt-in with no default, and a terminal outcome outlives the window until collected. |
| **D8** | The activity timestamp says whether it is activity, a transition, or no signal at all. Reported, never acted on. |

D2 is what keeps opencode inside the product boundary: instrumentation is the
launcher's act, so birddog never edits config and never restarts a session.
Coverage is limited to sessions launched with the plugin, and the adapter says
so.

## Open questions to investigate

None block Phase 1.

1. ~~`~/.codex/ipc/ipc.sock` — a control socket?~~ **Probed 2026-09-21: no.**
   It belongs to the Codex desktop app; the daemon locks are stale. The Codex
   adapter spawns its own `app-server` child. See [providers.md](./providers.md#codex).
2. `peerFeatures: ["notify_idle", ...]` in the Claude Code session registry —
   push instead of poll?
3. `TeammateIdle` — does it exist in Claude Code 2.1.267?

## Releasing

birddog is a Go binary and publishes no npm package of its own. The one
package it ships is the opencode plugin — see [RELEASING.md](../RELEASING.md)
and [ci-cd-standard.md](./ci-cd-standard.md).

## Conventions

- Docs live here, as Markdown.
- Claims about installed software are marked **[verified]** or **[unverified]**,
  and verified claims record how they were checked. Version numbers are pinned;
  findings are not claimed beyond the versions tested.
- A document written against published vendor docs is a draft. A document
  written against the running program supersedes it.
- Nothing machine-specific in a public repo: no tokens, no key material, no
  absolute home paths, no live session identifiers. Sample records carry
  placeholders, marked as such at the point of use.
