# birddog — review of the Rev 1 handoff

> Rev 1 · 2026-09-21
>
> Reviews [handoff.md](./handoff.md) against what the installed runtimes
> actually expose. [providers.md](./providers.md) states those capabilities;
> this document is judgment and consequence.
>
> **The two decisions this review asked for have since been made** — see
> [decisions.md](./decisions.md). §3 and §6 below are preserved as the
> argument that produced them, not as live questions.

## Summary

The design discipline is strong and should be preserved wholesale. The
integration assumptions are where it breaks, and they break in a way that
changes the shape of three sections rather than just their details.

Three sections need rewriting — **Codex**, **OpenCode**, **Orchestrator
notifications**. One needs promoting — **Claude Code**, which is cheaper and
more capable than the handoff assumes. Everything else stands.

## What is right, and should not be touched

Worth stating plainly, because the rewrite below should not disturb any of it:

- **The observation-only boundary** is drawn crisply and held consistently
  across nine sections without drift. That is rare at this length and it is the
  document's main asset.
- **`last_contact_at` separated from `last_activity_at`**, with the explicit
  note that a hook collector's timer cannot prove the agent is alive. This is
  the distinction most monitoring tools collapse.
- **Session-generation and config-revision stamps** on every event, so a
  delayed observation cannot overwrite newer state or be misattributed to a
  replacement session.
- **Refusing a derived `unblocked` flag**, and reporting request visibility as
  observed / not observed / unavailable.
- **Incident dedup persisted across restart**, with reminders off by default.
- **Monotonic time for live timers, wall clock for persisted records** — the
  correct split, and the reason sleep/wake handling is tractable at all.
- **Acceptance criteria 5, 6, 7, 14 and 16** are written against specific
  failure modes rather than happy paths. Keep them exactly as worded.

## 1. Claude Code is under-specified in its own favour

The handoff builds the Claude adapter entirely on hooks and flags "determine
whether changes affect already-running sessions before claiming attachment
success" as an open risk. Findings §2: the risk is already resolved by a live
registry the handoff does not mention.

`~/.claude/sessions/<pid>.json` supplies attachment, runtime `status`,
separate freshness timestamps, `sessionId` distinct from `pid`, and `procStart`
— which is precisely the "PID plus process start identity" that acceptance
criterion 7 demands, handed over rather than built.

**Change.** Recast the Claude Code section: registry-based attachment is the
mechanism; hooks are an enrichment layer for tool start/stop and permission
requests, which the registry does not carry. This also makes the honest
capability report better — attachment succeeds for every running session
instead of only for those started after a config edit.

## 2. Codex targets the wrong interface

The handoff's Codex plan investigates App Server `thread/status/changed` and
turn/item events. Findings §3: on 0.155.1 the App Server does not serve
external observers — `thread/loaded/list` reports only threads loaded in the
calling process.

The working route is `thread/list` against shared state, with liveness from
which process holds the thread's writer lock.

**Two consequences the handoff should absorb.** First, birddog must spawn its
own long-lived `codex app-server` child to read shared state — that is not
attachment, and it is a per-instance process cost that belongs in the
architecture note, not discovered during implementation. Second, tool events
and permission waits are simply not observable for Codex; the support matrix
cell is "unavailable," and the adapter must say so rather than degrade quietly.

**Before committing:** probe `~/.codex/ipc/ipc.sock` and
`~/.codex/app-server-daemon/`, both of which postdate tincan's "no control
socket" finding. A real control socket would be materially cheaper than
spawning a child. This is the single highest-value open question.

## 3. opencode contradicts the product boundary

This is the finding that changes scope rather than approach.

Findings §4: a default opencode TUI opens no port, writes no PID or lock file,
and exposes nothing an external process can find. Observation requires a plugin
running inside the session — which means editing `opencode.json` and restarting
the session.

The handoff forbids that three separate ways: passive observation only, hook
installation must "preserve existing configuration," and worker lifecycle
control is out of scope. There is no reading of the current text under which
opencode support is implementable.

**Decided — [D2](./decisions.md#d2--opencode-is-in-scope-instrumented-at-launch-by-muster): option 2, via muster.**
[muster](https://github.com/BrutalSystems/muster) already installs opencode
plugins from npm at launch, so instrumentation is the launcher's act and
birddog stays passive. Coverage is limited to sessions launched with the
plugin, reported as an honest capability limitation. The options as weighed:

1. **Drop opencode from the MVP.** Report it as an honest capability limitation
   — which acceptance criterion 13 already anticipates.
2. **Require pre-instrumented launches.** opencode targets are only watchable
   if the plugin was installed before the session started. Preserves the
   boundary; limits coverage to sessions the user prepared.
3. **Relax the boundary for opt-in instrumentation.** birddog may install the
   plugin with explicit consent, and the session must be restarted by its
   human — never by birddog.

Option 2 costs nothing and breaks nothing — and muster makes it the natural
deployment path rather than a concession. See
[providers.md](./providers.md#opencode)
for the three packaging traps that come with it.

## 4. The notification design does not survive contact with tincan

Findings §5. Two independent problems.

**tincan is not callable.** It is a stdio MCP server with no CLI surface, so
birddog must embed an MCP client and speak JSON-RPC to a spawned node process.
The Portability section recommends Go for subprocess management without
accounting for this.

**tincan points away from the orchestrator.** It reaches Codex and opencode
only, and explicitly declines Claude Code on the grounds that the host already
reaches its own sessions natively. But the orchestrator *is* a Claude Code
session in the Definition of Done. The "native route or tincan" fork therefore
has one live branch: the Claude Code inbox socket — which means reimplementing
tincan's Claude adapter rather than calling tincan.

**Change.** Rewrite the section around the inbox socket as the primary route,
with tincan named only as the reference implementation to read. Keep the
polling fallback — with no transport at all, `events` long-polling already
satisfies the Definition of Done, and the handoff is right that the durable
feed is the recovery path.

Worth keeping: tincan's delivery semantics (findings §5.3) validate the
handoff's own requirements. Fire-and-forget with a stable `message_id`, a log
recording delivered/held/dropped, and guaranteed queueing rather than
interruption. If the inbox route offers the same three properties, the
`delivery_unknown` design works as written.

## 5. Smaller corrections

- **The 30-second idle default will be noisy on Claude Code.** `Stop` fires at
  the end of every turn, so a session waiting on its human trips idle once per
  turn, per target. Either default idle alerts off, or set the grace in
  minutes. The registry's `status: idle` plus `statusUpdatedAt` is a better
  signal than hook-derived idleness anyway.
- **The workflow has no step one.** The config requires an explicit
  `session_id`, but nothing tells the orchestrator how to obtain one. `doctor`
  reports adapter capabilities, not candidate sessions. Add a read-only
  `birddog discover` that lists attachable sessions with their IDs — for
  Claude Code it is a directory read, and the Definition of Done ("start
  birddog for these three sessions") is unreachable without it.
- **`endpoint_ref` may be vestigial.** opencode has no endpoint; Codex has no
  reachable one. Check whether it earns a slot in schema v1 before freezing the
  schema, since unknown fields are to be rejected per version.
- **`TeammateIdle` is unconfirmed** in 2.1.267. Verify before it reaches the
  adapter contract.
- **Investigate `peerFeatures: ["notify_idle", ...]`** before building a
  polling loop — the harness may push idle rather than requiring a poll.
- **The desktop column is empty and untested.** The support matrix should ship
  with it blank rather than inferred.

## 6. The language decision is really a reuse decision

The handoff asks for the language choice to be recorded before implementation,
and offers Go for distribution and process management. That framing misses the
actual input: **discovery for all three providers already exists, in
TypeScript, in tincan** — registry parsing, socket probing, writer-lock
liveness, naming, and the peer-state model.

Phase 1's "process identity checks" and per-provider session discovery largely
*are* that code. Choosing Go means reimplementing it and maintaining two copies
that will drift, and tincan's are the copies with verified findings behind
them. Choosing TypeScript means the discovery layer can be extracted and shared,
at the cost of a heavier runtime for a background daemon.

A third option worth pricing: keep birddog in Go, and have it shell out to a
small read-only discovery command extracted from tincan. That keeps one
implementation of the hard part without coupling the daemon to node — but it
requires tincan to grow the CLI surface §5.1 shows it does not have.

**Decided — [D1](./decisions.md#d1--go-with-discovery-implemented-in-birddog): Go, discovery reimplemented in birddog, independent of tincan.**
The two tools have independent release cycles and ask different questions of
the same runtimes — tincan asks "who can I message," birddog asks "what is
this session doing." The accepted cost is two implementations that will drift;
the mitigation is a shared statement of what each runtime exposes, which is
what [providers.md](./providers.md) is for.

## 7. Recommended order

1. ~~Decide opencode scope and the language/reuse question.~~ Done —
   [decisions.md](./decisions.md).
2. Probe `~/.codex/ipc/ipc.sock` before designing the Codex adapter.
3. Rewrite the three sections above into a build spec; promote Claude Code
   from hooks-first to registry-first.
4. Build Phase 1 as written — generic process/file monitoring, SQLite, cursor,
   policy — which is unaffected by all of the above and independently useful,
   exactly as the handoff claims.
