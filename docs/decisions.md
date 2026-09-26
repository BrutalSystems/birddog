# birddog — decisions

> Rev 1 · 2026-09-21
>
> The handoff asks that the language choice be "recorded before
> implementation." This is that record, extended to the other decisions the
> [review](./review.md) surfaced. Each entry states what was decided, what it
> rules out, and what it costs.

## D1 — Go, with discovery implemented in birddog

**Decided.** birddog is written in Go. Session discovery for all three
providers is implemented here, in Go, from scratch.

**Rejected:** writing birddog in TypeScript to share
[tincan](https://github.com/BrutalSystems/tincan)'s existing discovery layer;
and having birddog shell out to a discovery CLI extracted from tincan.

**Why.** birddog and tincan are independent tools with independent release
cycles. A shared discovery library couples them: tincan's discovery exists to
answer "who can I message," birddog's to answer "what is this session doing,"
and those questions diverge — birddog needs freshness, state transitions and
PID-reuse guards that a messaging tool has no reason to carry. Go also suits a
long-lived background daemon better than Node.

**Cost, accepted.** Two implementations of provider discovery now exist and
will drift. The mitigation is not shared code but a shared statement of the
ground truth: [providers.md](./providers.md) records what each runtime exposes,
so both implementations can be corrected against the same description. When a
runtime changes, that document is what gets updated first.

**Layout.** A polyglot repository, because the opencode plugin (D2) cannot be
Go:

```
go/                  daemon, CLI, adapters, policy, store
plugins/opencode/    the birddog opencode plugin (TypeScript, published to npm)
docs/
```

This mirrors tincan, which carries a Go-free equivalent of the same split.
*Provisional* — one `git mv` if a flat Go layout at the root is preferred.

## D2 — opencode is in scope, instrumented at launch by muster

**Decided.** opencode is supported through a birddog plugin running inside the
session, published to npm as `@brutalsystems/birddog-opencode` and installed at
launch by [muster](https://github.com/BrutalSystems/muster)'s `npm` plugin
specifier. birddog itself never installs the plugin, never edits
`opencode.json`, and never restarts a session.

**Rejected:** dropping opencode from the MVP; having birddog install the plugin
with consent.

**Why this resolves the boundary problem.** As [providers.md](./providers.md#opencode)
records, a default opencode TUI exposes nothing an external process can find, so
observation requires code inside the session. That looked like it forced a
choice between dropping opencode and violating the product boundary — birddog
does not mutate or restart sessions.

muster dissolves it. muster already launches instructed agents and already
installs opencode plugins by npm specifier at launch. The instrumentation
happens at launch time, by the launcher, which is muster's job — not
birddog's. birddog stays passive and still observes the session.

**Consequence for coverage, stated honestly.** birddog can observe an opencode
session **iff it was launched with the plugin** — in practice, launched by
muster with birddog selected. An opencode TUI a human started by hand is not
observable, and the adapter must report that as an honest capability
limitation rather than degrade quietly. This satisfies handoff acceptance
criterion 13 rather than evading it.

**Three traps to carry into the plugin build**, all from muster's own docs:

1. The opencode loader reads `exports["./server"]` and falls back to `main`. It
   **does not read `exports["."]`**. A package declaring only `.` is fetched,
   its `package.json` read, the session runs — and the plugin never executes,
   with nothing logged. Declare `./server`.
2. Prefer the `npm` specifier over a copied file. A copied plugin goes stale in
   silence. Record a `plugin_version` in every registry record so a session can
   be checked against what is installed.
3. `--plugin` necessarily relaxes muster's `--pure` isolation: opencode merges
   project-local `.opencode/plugin` discovery into any non-empty plugin list,
   so an instrumented child also loads whatever plugins the target repository
   ships. Select birddog only for repositories trusted with that.

## D3 — desktop apps are out of scope for v1

**Decided.** Claude, Codex and opencode desktop applications are not supported
in the first version. The support-matrix column stays blank rather than
inferred.

**Why.** Untested, and the handoff already warns against reverse-engineering
private desktop databases as an MVP contract. Terminal and
programmatically-launched sessions cover the actual use.

## D4 — public repository under BrutalSystems

**Decided.** `github.com/BrutalSystems/birddog`, MIT, public, matching tincan
and muster.

**Consequences, already applied.** No machine-specific identifiers in the
docs: the sample session record in the findings has placeholder `pid`,
`sessionId`, `cwd` and `name`, noted as such at the point of use — every key
and value *shape* is as observed. No tokens, no key material, no absolute home
paths. `.gitignore` excludes `probes/` and `.private/` so live-probe records
and working notes stay local, following muster's convention.

**The probe record stays out of the repository.** The raw record of what was
inspected on a developer's machine — installed versions, sample registry
records, the commands each claim was verified with — is local only, following
muster's handling of its own `PROBE_RESULTS.md`. What that record *established*
is public, in [providers.md](./providers.md); the machine it was established on
is not.

## D5 — while 0.x, the minor is the breaking-change signal

> **Superseded at 1.0.** birddog is on ordinary semver from 1.0.0: major for a
> change a consumer relies on, minor for a feature — new adapters and new
> commands included — patch for a fix. This entry is kept because its
> reasoning about *what counts as breaking* still holds, and because releases
> before 1.0 were made under the rule as written below and must be read under
> it. Only which number carries the signal has changed.

**Decided.** Bump the **minor** for a change to something a consumer can rely
on: the shape of an event or status record, the CLI surface a script calls
(commands, flags, `--json` output), or the *meaning* of a reported state —
what `unavailable`, `(stale)` or an idle report asserts. Everything else is a
**patch**, new adapters and new commands included. Reconsider at 1.0, where a
feature earns a minor.

**Rejected:** ordinary semver, which is tincan's rule; and having no rule,
which is what birddog had until this entry.

**Why.** birddog's contract is what it reports, not what it can do. An
orchestrator reads its events and decides whether to wait, so a change to what
a status *means* breaks a caller silently while a new adapter does not —
adding one leaves every existing reader correct. Reserving the minor for the
first kind makes the version number carry the signal a reader needs. This also
records existing practice rather than changing it: 0.1.3 added an install
channel and a `--version` command and shipped as a patch.

**Cost, accepted.** A birddog minor and a tincan minor do not mean the same
thing, and tincan's rule is the one most outside readers will assume. The
three repositories are independent by [D4](#d4--public-repository-under-brutalsystems)
and by the note at the top of [ci-cd-standard.md](./ci-cd-standard.md), so a
shared rule was never available without coupling them; what is available is
stating birddog's plainly, where a reader looks for it. muster documents the
same shape for its own reasons, which is convergence rather than a house
style — neither repository defers to the other.

## D6 — `no_progress` ships with no default, because the signal is not comparable

**Decided.** `no_progress` stays off until an operator sets
`no_progress_after_seconds`. Not because the right number has not been found
yet, but because there is no single number to find: what the threshold is
compared against means different things on different providers, so one default
would be two different conditions wearing one name.

**Rejected:** a conservative cross-provider default such as thirty minutes,
which is the smallest value the evidence below does not contradict. It was
rejected on what it would *detect*, not on its false-positive rate.

**Why.** The condition opens when `observed_at - last_activity_at` exceeds the
threshold while a provider reports work. Only an instrumented session restamps
that timestamp as work happens. Uninstrumented Claude Code takes it from the
session registry's status timestamp, which moves on a transition and not
otherwise.

Measured by replaying 39 instance stores — 189,318 events, 99 observed Claude
Code working runs, 6.82h of working time, no hooks installed anywhere:

- The activity timestamp never moved within the run in **98 of 99 runs**.
- Run duration and peak staleness correlate at **r = 0.9954**.
- 97 of the 99 runs ended in a normal transition, so the corpus contains **no
  stall**: every threshold's firings in it are false, and it can establish a
  floor but never that a threshold catches anything.
- Runs a threshold would have fired on: 60s → 77, 5m → 31, 10m → 13, 15m → 5,
  20m → 1, 30m → 0. At 60s, eight runs open the condition on the *first*
  observation of the run, before the session could have made any progress.

So on that signal the duration measures the age of the turn, and the condition
is a turn-duration alarm: a wedged session and a busy one are indistinguishable
because the distinguishing information is not in the feed. Thirty minutes is
the floor the evidence permits, and it is also barely above the longest
legitimate run observed — the next long test suite clears it — while detecting
only that a turn ran long, which is not what the name promises a consumer.

An instrumented opencode session restamps every 1–8s, where the same duration
does measure silence and a much shorter threshold would be right. Two
providers, two meanings, one field: a default would have to pick one and be
wrong about the other silently.

**Cost, accepted.** The condition is off for everyone who does not already
know it exists, which is most of the value left on the table, and that is the
cost this entry pays rather than removes. What replaces a default is saying
plainly what the number means per signal, in
[docs/cli.md](./cli.md#attention-conditions) and beside `NoProgressAfter`. The
underlying conflation — an activity field that carries a transition timestamp,
with nothing in the record saying which — is a separate problem and is filed
as [#37](https://github.com/BrutalSystems/birddog/issues/37); if it is fixed,
a default becomes answerable per signal kind and this entry should be revisited.

Measurement recorded in [#27](https://github.com/BrutalSystems/birddog/issues/27).

## D7 — event retention is opt-in, and a terminal outcome outlives it

**Decided.** An instance keeps every event unless `retention.max_age_seconds`
is set, and setting it requires `retention.hold_uncollected_seconds` in the
same breath. A verified exit nobody has acknowledged survives the window by
that hold and is then dropped, which raises the retention floor so the next
replay from an older cursor fails with `cursor_stale`. Collected means
acknowledged through `ack` and nothing weaker.

**Rejected:** a default window, which would make an upgrade delete history that
was previously replayable. Rejected: deriving collection from how far a reader
has read, because birddog holds no reader's position and any store-level
stand-in is shared by every reader — one `birddog events` run would mark an
outcome collected on the orchestrator's behalf. Rejected: holding an
unacknowledged outcome indefinitely, which is a leak wearing a safety
argument.

**Why.** A terminal outcome is the only thing a consumer cannot re-observe: a
session that has exited will never report anything again, so an exit dropped
before it was collected is unrecoverable. Everything else in the feed can be
observed a second time. That asymmetry is the whole reason one row gets
treated differently from the rest.

No default, for the reason [D6](#d6--no_progress-ships-with-no-default-because-the-signal-is-not-comparable)
gives about `no_progress`: a number that deletes a consumer's history is one
nobody has justified. The pressure that would have argued for one is gone
anyway — a verified exit is now recorded once rather than restated on every
pass, which was 52.7% of all events in a measured corpus.

**Cost, accepted.** An operator who configures nothing keeps everything and can
fill a disk, which is the same shape of cost D6 accepts: the condition is off
for whoever does not know it exists. Retention is also all-or-nothing beneath a
held outcome — the floor is one contiguous boundary, so one held exit retains
every later event too until its hold elapses. A history with holes could not
answer `cursor_stale` honestly, and that answer is what makes any of this safe.

And the event feed is no longer a liveness signal for birddog itself. It was
one only by accident, because something was always being appended; an instance
whose targets have all exited is now silent. Liveness is the control channel
answering at all, which `status` provides.

## D8 — the activity timestamp says what it is

**Decided.** Every observation carries `activity_resolution` beside
`last_activity_at`: `activity` where birddog's own instrumentation is reporting
the session, `transition` where the value is the session's own status timestamp,
`unavailable` where there is no signal at all. It is reported and never acted
on — no condition reads it, and `no_progress` is unchanged.

**Rejected:** branching in the policy, so `no_progress` could carry a default
where the resolution is `activity`. That would answer
[D6](#d6--no_progress-ships-with-no-default-because-the-signal-is-not-comparable)
now, on four minutes of instrumented corpus. Rejected: declining to evaluate
`no_progress` on a `transition` timestamp, which silently removes a condition
some operators configured deliberately to catch a long-running turn. Rejected:
labelling each pass by whichever timestamp was latest, which would make the
value flip from observation to observation and be worse than no label.

**Why.** `LastActivityAt` is documented as "the last session-scoped action or
output" and for uninstrumented Claude Code it is the last *transition*. The
value is not fabricated — a transition is evidence the session did something —
but what is missing is its resolution: how stale it can be while the session is
perfectly healthy. Measured while answering D6, the timestamp never moved within
the run in 98 of 99 observed working runs, duration and staleness correlating at
r = 0.9954, with a healthy run reaching 27 minutes stale. So the same number is a
stall on one signal and an ordinary turn on the other, and until now both arrived
in one field wearing one name. This is the distinction the codebase already draws
for `observed` / `not_observed` / `unavailable`, applied to the one field that had
been left ambiguous.

**Cost, accepted.** It does not answer D6; it satisfies the precondition D6 named
and leaves the default to a later decision with the corpus D6 says is missing.
The value is a property of the observing arrangement rather than of the
individual reading, so it is stable across passes but says nothing about any one
of them. And a consumer that ignores it is exactly as informed as before — this
buys nothing for anyone who does not read it.

## What remains open

Neither blocks Phase 1. See [docs/README.md](./README.md).

- ~~**`~/.codex/ipc/ipc.sock`**~~ — probed 2026-09-21, and it is the Codex
  desktop app's socket, not a CLI control socket. The spawned
  `codex app-server` child is confirmed necessary.
- **`peerFeatures: ["notify_idle", …]`** in the Claude Code session registry —
  push instead of poll?
