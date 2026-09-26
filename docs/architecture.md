# Architecture

birddog is a CLI and a small background daemon, one per monitoring instance. Go,
no cgo, one dependency (a pure-Go SQLite driver).

## The shape of a pass

```
discover/*      what sessions exist, and which are live
    │
observe/*       adapt one provider's view into an Observation
    │
policy          which attention conditions hold, given the target's rules
    │
store           the event log, the derived state, the incidents
    │
daemon          the loop, the control channel, the lock
    │
cli             commands and presentation
```

Each layer depends only on the ones above it. The policy has no clock, no I/O
and no knowledge of any provider — the caller passes the time and an
`Observation`, which is why every threshold is tested exactly rather than
approximately.

## Packages

| Package | Responsibility |
|---|---|
| `discover/claude`, `discover/codex` | Find sessions and decide which are live |
| `observe` | Turn one provider's view into an `Observation`, including what it cannot see |
| `policy` | Deterministic: observations plus rules produce conditions to open and close |
| `store` | Append-only events, derived per-target state, incidents. SQLite |
| `monitor` | One observation pass over every target |
| `daemon` | Instance lifecycle, the loop, the control channel |
| `ipc` | One JSON request per connection over a unix socket |
| `config` | Strict, all-or-nothing parsing of the watch list |
| `instance` | The machine-wide index of instances |
| `platform/*` | Process identity, paths, locking — the parts that differ per OS |
| `cli` | Commands, JSON and human output |

Platform-specific code is confined to `platform/` and the `_darwin.go` files.
Nothing above it knows what a `sockaddr_un` is.

## Decisions that shaped it

**Evidence decides liveness, not the presence of a record.** A Claude Code
registry entry outlives its process, so the socket is probed *and* the process
identity checked. A Codex thread is live only while a running process holds its
writer lock. A lock file, a registry record and a socket file all survive a
crash; none of them is proof of anything.

**Two clocks, deliberately.** Wall-clock timestamps go in the durable record.
Thresholds are evaluated from timestamps the caller supplies, so a fake clock
drives them in tests.

**The log and the derived state share a transaction.** An event is appended and
the target's state updated together, so they cannot disagree about what was
accepted. A late observation from a replaced session is still logged — it
arrived, and that is a fact — but it does not become the replacement's state.

**Incidents are deduplicated by the database.** A partial unique index over
(target, condition, generation) where unresolved, rather than a check every
caller must remember.

**State and sockets live apart.** Databases go in
`~/Library/Application Support/birddog`. Sockets cannot: macOS caps a unix
socket path at 104 bytes and that directory plus an instance id measures 107.
Sockets go in a short per-uid directory under `/tmp`.

**Nothing can reach a worker.** The control channel serves `status`, `events`,
`ack`, `stop` — and nothing else. There is no code path from birddog to a
watched session's input, and a test asserts that eight plausible names for one
are unknown methods.

## What runs where

One process per instance, in its own session so it outlives the shell that
started it. It holds an advisory flock for the life of the instance; a second
daemon for the same instance is refused rather than allowed to duplicate every
alert. It observes on a timer, and answers its socket on a goroutine per
connection so a long poll blocks nothing else.

Reading Codex metadata spawns a `codex app-server` child, because that version
serves no external observer any other way. It is spawned only when there is a
live thread to describe, and torn down immediately.
