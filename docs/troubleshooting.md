# Troubleshooting

## `birddog discover` shows nothing

No agent sessions are running, or none that birddog can see. Check what is
observable at all:

```sh
birddog doctor
```

A hand-started opencode TUI will never appear: it exposes nothing an external
process can find. Launch it through muster with the birddog plugin, and check
`~/.birddog/opencode/` for its record. See
[providers.md](./providers.md#opencode).

## A session shows `(stale)` though it is running

Liveness needs two things to agree: the session's socket must answer, and the
process at its pid must be the one recorded. Either failing marks the state
stale rather than hiding it.

Most often the socket directory is gone (`/tmp` was cleaned) or the session was
restarted, which makes it a new run. Re-run `birddog discover` to pick up the
current identity.

## A Codex thread shows `unavailable`

Expected. Codex reports a thread as `notLoaded` to any process that does not own
it, which describes the asking process rather than the session. birddog will not
repeat it as a session state, so it says the state is unavailable. The thread is
still known to be live — that comes from its writer lock, not from the state
database.

## `start` says the instance did not become reachable

The daemon logs to `~/Library/Application Support/birddog/logs/<instance>.log`.
Read that first; the failure is usually a config error or a permissions problem
on the state directory.

## `[not_running] no instance is listening`

The instance is not up. `birddog list` shows whether its process is alive. A
record with `RUNNING no` is left over from a daemon that crashed; starting again
is safe — a stale lock file does not block a restart, because the lock lives on
an open descriptor the kernel drops when the holder dies.

## `[cursor_stale]`

Replay from that cursor would silently lose events that have since been pruned,
so it fails instead. Take a fresh `status`, use the cursor it reports, and
accept that the gap happened rather than pretending it did not.
Events are dropped only when `retention.max_age_seconds` is set on the
instance; without it nothing is ever pruned and a cursor cannot go stale this
way.

## A target reports `input_request_visibility: unavailable`

For Claude Code, install the hooks: `birddog hooks install`. Sessions already
running normally pick them up within a minute or two; start a new session if
one does not. `birddog hooks status` tells you whether the installation itself
is complete.

Otherwise accurate for Codex, which exposes no such signal.

**It does not mean the worker is unblocked.** The whole reason the field is
worded this way is that an absence of observation must never be reported as
evidence of absence. An instrumented opencode session reports `observed` or
`not_observed` instead — the difference between looking and being unable to.

## Hooks are installed but nothing changed

Check `birddog hooks status`. If it reports a partial install, re-run
`birddog hooks install` — partial coverage looks like coverage and is worse
than none.

If it reports installed, give it a minute — a running session picks hooks up
shortly after they appear. Start a new session if it still shows nothing.

The handler is deliberately silent: it exits 0 and prints nothing whatever
happens, so a failure leaves no trace in the transcript. Look in
`~/.birddog/claude-hooks/` for the session's markers to see whether anything
is arriving.

## An opencode session shows no record

Check `~/.birddog/opencode/` and the plugin's own log at
`~/.birddog/opencode-plugin.log`. The plugin cannot print to the terminal:
`console.error` would land in the TUI opencode is drawing on.

The usual cause is the plugin not being loaded at all. Confirm the version it
reports matches the release you expect:

```sh
grep plugin_version ~/.birddog/opencode/*.json
```

If it reports an older version, opencode is serving a cached copy. It resolves
an npm specifier once and does not revisit it, so a new release is not picked
up until the cache is cleared:

```sh
rm -rf ~/.cache/opencode/packages/@brutalsystems/birddog-opencode@latest
```

## Quiet alerts never fire for Claude Code

By design. Claude Code stamps a session's status only when it changes, so a
session busy for an hour carries an hour-old timestamp — reading that as silence
would report a stall nobody observed. A status that says the session is working
is evidence of work. Quiet is for a reachable session whose state says nothing
useful and where nothing has been seen to happen.

## A `no_progress` alert arrived and the session looks fine

Read `activity_resolution` in the event's evidence. It says what the activity
timestamp the threshold measured against actually is, and the answer changes what
the alert meant:

- **`transition`** — the timestamp moves only when the session changes state, so
  the duration you set was measured against the age of the current turn, not
  against silence. A long turn trips it. Either set the threshold above the
  longest turn you expect, or install the instrumentation that makes the signal
  finer: `birddog hooks install` for Claude Code.
- **`activity`** on a `process` target — the signal is a watched file changing,
  which is as coarse as whatever writes it. A build that buffers its log can look
  stalled while it is working. Set the threshold from how those files actually
  get written.
- **`unavailable`** — nothing is watching this session's activity, so the
  condition does not evaluate at all and the alert cannot have come from this
  target. `no_progress` requires an activity timestamp to measure from. To make it
  work here, give birddog something to watch rather than tuning the threshold.

## Idle alerts are noisy

Turn them off; they are off by default for this reason. A `Stop` event ends
every Claude Code turn, so a session waiting on its human looks idle within the
grace period and alerts once per turn. Either leave `idle` out of `alert_on`, or
set `idle_grace_seconds` in minutes.

## A worker will be quiet for a while on purpose

Say so, rather than having birddog report a silence you already expect:

```json
"policy": {
  "expected_quiet_until": "2026-09-21T18:30:00Z",
  "expected_quiet_reason": "running the full integration suite"
}
```

It suppresses quiet and idle only. Exit, input requests and observation loss
still surface, observations are still recorded, and when the override lapses the
thresholds start fresh rather than firing immediately about the silence you
authorised.

## Alerts are not being delivered anywhere

Check what is configured. With no `notification` block, or `{"kind": "none"}`,
nothing is delivered by design and alerts are polled. `birddog doctor` lists
the routes this build offers; an unknown kind is refused at startup rather
than silently ignored.

For a Claude Code orchestrator, name the session alerts should go to:

```json
"notification": {"kind": "claude-inbox", "options": {"session_id": "…"}}
```

Take that `session_id` from `birddog discover`. Alerts are queued for the
session's next turn boundary, so they never interrupt it — which also means
they appear when that turn ends, not instantly.

If delivery is failing, `birddog doctor` and the instance log will say why;
the usual causes are a session that has since restarted (its socket and token
change) or one that has exited.

Polling always works regardless — this is the intended fallback, not a
workaround:

```sh
birddog events --instance <id> --after <cursor> --wait 25 --json
```

The feed is the recovery path under every transport, so nothing is lost by
using it directly.

## Instances I forgot about are still running

Expected, and deliberate: an instance outlives the session that started it, so
that monitoring survives an orchestrator's turn ending. Nothing reaps one that
was never stopped.

```sh
birddog list                     # RUNNING says which are live
birddog stop --instance <id>     # ends a live watch
birddog prune --dry-run          # forgets the records of ones already over
```

To have them clean themselves up, tie the instance to the session that wants
it:

```sh
birddog start --config x.json --owner "$(birddog discover --json | …)"
```

It then stops when that session is gone. Left unowned, it waits — which is
what you want for a watch spanning several conversations, and not what you
want for one.

## Stopping birddog — what happens to the workers

Nothing. birddog never held anything of theirs. Its own socket, lock and
registry record are given back; the instance's database is kept.
