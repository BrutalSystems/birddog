# CLI reference

Every command takes `--json` for machine-readable output. Errors carry a stable
code, a readable explanation, and a nonzero exit status (`1` for a failed
operation, `2` for a bad invocation).

birddog only observes. No command here sends anything to a watched session,
approves anything on its behalf, or starts, stops or restarts one.

## `birddog discover [--json]`

Lists the sessions on this machine that can be registered as targets. This is
where a `session_id` comes from.

```
NAME          PROVIDER  STATUS       PID    SESSION       CWD
api-refactor  claude    busy         41207  11111111-…    /work/api
auth-thread   codex     unavailable  41880  01a0c46d-…    /work/auth
```

A session is listed as live only on evidence — for Claude Code, its socket
answers *and* the process at its pid is the one the registry recorded; for
Codex, a running process holds the thread's writer lock. Where the evidence
fails, the last observed status is shown and marked `(stale)`.

`unavailable` is an answer, not a gap. Codex reports its threads as `notLoaded`
to anyone who does not own them, which describes the asking process rather than
the session, so birddog says it cannot see the state instead of guessing.

For Claude Code, copy `pid` and `proc_start` into the target's attachment
alongside `session_id`: they are what let birddog tell a finished session from
one it merely cannot see.

## `birddog start --config FILE [--json] [--foreground] [--owner SESSION]`

Starts a monitoring instance and returns once it is actually reachable — not
merely once the process has been spawned.

The instance runs in its own session, so it outlives the shell that started it.
`--foreground` keeps it in the current process for debugging.

**`--owner` ties the instance to a session**, given as a session id from
`birddog discover`. When that session is gone the instance shuts down on its
own, releasing its socket and lock and marking its record stopped.

Without it, an instance outlives everything and waits to be stopped
explicitly. That is the default because it is right for a watch spanning
several conversations — and wrong only when the watch was for one. Nothing
else reaps an instance, so an orchestrator that closes for good leaves its
instance running with nobody to read the answers.

Liveness uses the same check as a watched Claude Code session: the registry
record, a socket probe, and the process identity, so a reused pid cannot keep
an instance alive for a session that ended. Three consecutive absences are
needed — one missed check is not evidence, and a session is briefly
unobservable while it restarts. A start naming a session that does not exist
is refused rather than accepted and reaped moments later.

The result carries the instance id, where its state lives, and the cursor to
follow from.

## `birddog status --instance ID [--json]`

Current state of every target, with its open attention conditions and the
newest cursor, so a snapshot and the event feed cannot miss anything in
between.

`status_is_current` says whether a status may be read as a present fact. When
it is false, the status is the last thing observed and is history.

`input_request_visibility` is `observed`, `not_observed` or `unavailable`. It
never says a worker is unblocked. Claude Code and an instrumented opencode
session can report a request; Codex cannot, and says `unavailable` rather than
implying there is none.

Where a request is observed, `input_request_kind` carries what the provider
called it — `bash`, `edit`, `permission prompt` — verbatim, so a consumer can
branch on it without parsing prose. Kinds are not normalised across providers:
doing so would assert an equivalence nobody established.

`reason` says why an answer is not current and readable, and is absent when it
is:

| Reason | Meaning |
|---|---|
| `contact_lost` | The session could not be reached this pass. It may still be running |
| `source_unreadable` | What birddog reads to observe this target could not be read |
| `status_unrecognised` | Reached, but reported a value whose meaning is not established |
| `provider_limited` | Cannot be observed from outside for this provider, at any version |

Only the last will not change by asking again.

## `birddog events --instance ID [--after CURSOR] [--store ID] [--wait SECONDS] [--limit N] [--json]`

Observations since a cursor, oldest first. `--wait` holds the request open
until something happens or the wait elapses; an elapsed wait returns an empty
result, which is an answer rather than a failure.

The wait is bounded at 25 seconds. A longer one is held for the bound and the
response says so in `wait_clamped`, because returning early while reporting
nothing is indistinguishable from having waited and seen nothing. The bound
sits below the thirty-second idle timeout of the transport intended for
cross-machine delivery, so the same command is correct over either path.

### After a verified exit

A verified exit is recorded **once**. Later passes still observe the target — a
session replaced at the same target id is still noticed — but write nothing
while the answer is unchanged, so that target's feed goes quiet and `--wait`
against it waits out its bound instead of returning a restatement. Anything
that differs resumes recording: a new generation, a status that is not
`exited`, or contact being lost.

`last_seen_at` on an exit incident stops advancing for the same reason. A
condition that cannot lapse has nothing to refresh.

The target's `observed_at` freezes with it: state is written from a recorded
observation, and after an exit there are no further records. It is when birddog
last *recorded* something about the target, not when it last looked — birddog
goes on observing an exited target on every pass. Reading it as "when birddog
last looked" is exactly the mistake the paragraph above warns about, and on an
exited target it grows without bound while nothing is wrong.

**The feed is not a liveness check for birddog itself.** An instance whose
targets have all exited is silent, and in the feed that is indistinguishable
from an instance that has stopped. Liveness is the control channel answering at
all, which `status` provides and which does not depend on anything having
happened.

A cursor that has fallen behind the retained history fails with
`cursor_stale` rather than silently returning whatever remains.

A cursor is a position in one store, so carry the `store` identity reported
beside it and pass it back with `--store`. If the instance's store has since
been replaced — birddog reinstalled, its state directory cleared — the request
fails with `store_replaced`. Without it the old cursor would pass the
staleness check, match nothing, and return an empty page: a consumer would be
told there is nothing new, which is indistinguishable from every watched
session having gone quiet.

### Machine identity

A cursor is a position in one store on one machine. `store` says which history;
`machine` says which host. A consumer holding feeds from more than one machine
needs both, because the same cursor value in two feeds is two different
positions and nothing in the number says so.

birddog does not mint this identity — a hostname is neither stable nor unique,
and a replacement standing in the same place reuses it. It is supplied:

```sh
BIRDDOG_MACHINE=ferry:7c3a91b2 birddog daemon --config ...
```

Unset is **local mode**: no `machine` key appears in any output, and behaviour
is exactly as it was before the field existed. birddog imposes no format on the
value beyond it being printable, unspaced and at most 128 bytes; the scheme
belongs to whatever supplies it. A malformed value fails the daemon at start
rather than being dropped, because a daemon that ignored it would serve
unqualified cursors while its operator believed otherwise.

The field appears on `status` (and on each target, where it qualifies the
session reference — a sibling field, never concatenated into `session_id`), on
a page of events, and on each event: an observation carries the machine it was
made on, so it stays true once the event leaves the machine that recorded it.
An event recorded before this existed carries no machine rather than being
back-filled with one it was never observed under.

Passing `--machine` with a cursor makes the request refusable. If it names a
different machine, or this instance has no identity configured and so cannot
confirm it is the one named, the request fails with `machine_mismatch` rather
than answering from a history that belongs to another host. A caller that sends
none gets the behaviour it had before identities existed, which is what keeps
this additive.

## Retention

An instance keeps every event it has recorded. Set both keys in the instance
config, beside `notification`, to bound that:

```json
"retention": {
  "max_age_seconds": 604800,
  "hold_uncollected_seconds": 86400
}
```

`max_age_seconds` is how far back the event log is kept. There is deliberately
no default: a window that deletes a consumer's history is a number nobody has
justified, which is the reasoning
[D6](./decisions.md#d6--no_progress-ships-with-no-default-because-the-signal-is-not-comparable)
records for `no_progress`.

`hold_uncollected_seconds` is **required** whenever `max_age_seconds` is set,
and either key alone is refused. It is how much longer a terminal outcome
nobody has collected survives past the window, measured from the moment the
window would have dropped it. A terminal outcome is the one thing a consumer
cannot re-observe — a session that has exited will never report anything again
— so it is held rather than dropped on time; holding it forever for a consumer
that will never return would be a leak, so the bound is stated rather than
assumed.

**Collected means the exit incident has been acknowledged** through `ack`.
Nothing else counts: birddog does not track any reader's position, so it cannot
know what was received without being told. Where a target's policy does not
list `exit` in `alert_on` there is no incident to acknowledge, and its terminal
outcome is held for the bound and then dropped.

**One held outcome holds everything after it.** Retention is a single
contiguous floor, so pinning it below a held exit keeps every later event too,
including unrelated ones, until the hold elapses. A history with holes in it
could not answer `cursor_stale` honestly, and that answer is what makes any of
this safe.

When a held outcome is finally dropped the floor rises, and a replay from an
older cursor fails with `cursor_stale` rather than returning a page that looks
complete. A consumer is told it lost something.

The window is enforced once when an instance starts and periodically after, so
an instance that configures one applies it immediately rather than at the end of
its first interval. Enforcement is idempotent: the retained history only ever
shrinks, so repeating a pass cannot restore anything or drop anything twice.

`birddog prune` is unrelated: it forgets the records of instances that are
already over, not events.

## Attention conditions

What a target can be configured to surface, in `policy.alert_on`. A condition
not listed is neither opened nor closed — birddog does not resolve incidents it
was never asked to raise.

| Condition | Opens when |
|---|---|
| `input_requested` | A permission or input wait was observed |
| `exit` | The session is verifiably gone |
| `observation_lost` | Contact was lost. The absence of evidence, never an exit |
| `quiet` | Nothing was observed to happen, and nothing else explains it |
| `idle` | The session reported idle for longer than the grace period. Opt-in |
| `no_progress` | The session asserts work, and nothing has happened for a long time |

`no_progress` exists because `quiet` is deliberately suppressed while a
provider reports work: a provider need not restamp a status that has not
changed, so a legitimately busy session carries an old timestamp, and reading
that as silence would report a stall nobody observed. The cost is that a
session **wedged** while reporting work raises nothing at all.

It has **no default** and does nothing until `no_progress_after_seconds` is
set. The threshold is the whole design — too short reintroduces exactly the
false stalls the suppression prevents, and too long never fires — and what is
appropriate depends on the work being watched.

**What the threshold measures is not the same quantity on every provider**, and
every observation now says which it is. `activity_resolution`, in an event's
evidence beside `last_activity_at`:

| value | what the timestamp is | what a threshold detects |
|---|---|---|
| `activity` | something is watching this session do things: tool boundaries for Claude Code with hooks and opencode with the plugin, a watched file changing for a `process` target | silence, at that signal's own resolution |
| `transition` | the session's own status timestamp, which moves only when the state changes | how long the turn has run |
| `unavailable` | nothing is watching this session's activity | nothing — the condition does not evaluate |

Claude Code reports `transition` until `birddog hooks install` has been run and a
hook has reported the session, and `activity` after. An opencode session launched
with the plugin reports `activity`. A `process` target reports `activity` when it
names any `observations.files` or `observations.logs`, and `unavailable` when it
names none. Codex reports `unavailable` on every path.

**`activity` is not one resolution.** A tool boundary is seconds; a watched file
changing is as coarse as the thing writing it — a compile that buffers its log, a
test runner that flushes at the end, a linker that writes nothing for minutes.
Set a `process` target's threshold from how its files actually get written, not
from how long a tool call takes.

**`no_progress` requires an activity timestamp.** On a target reporting
`unavailable` there is no moment to measure silence from, so the condition never
opens however the threshold is set — a duration measured from nothing is not a
duration. Setting `no_progress_after_seconds` there is inert rather than harmful,
and if you want the condition on that target, give birddog something to watch:
hooks for Claude Code, the plugin for opencode, or `observations.files` and
`observations.logs` for a `process` target.

The value names what is watching rather than which timestamp was latest on a
given pass, so it does not flip between observations while the arrangement
holds.

Measured over 99 observed Claude Code working runs with no hooks installed,
the timestamp never moved within the run in 98 of them, and staleness tracked
the age of the turn almost exactly (r = 0.9954), reaching 27 minutes on a run
that then ended normally. On that signal `no_progress` is a turn-duration
alarm rather than a stall alarm: a wedged session and a busy one are
indistinguishable, because the distinguishing information is not in the feed.
Set the threshold above the longest turn you expect, not the longest tool
call, and read what it raises as *this turn has run long*. With activity
instrumentation in place it means what its name says, and a far shorter
threshold is appropriate.

Codex usually reports no readable status, so the condition rarely applies to it
at all.

After the machine wakes, or after birddog regains sight of a target it had
lost, duration-based conditions restart from that moment. Silence birddog did
not witness is not a duration it may measure. `exit` and `input_requested` are
facts rather than durations and still surface immediately.

When **every** target becomes unobservable in the same pass, the events record
`correlated_loss`. That is the shape a dead adapter child or an unreadable
registry produces, and the sessions are most likely fine and merely out of
view — so it is stated as one fault to look at rather than left to be inferred
from N separate ones. birddog does not claim which cause it was.

## `birddog ack --instance ID --incident N [--json]`

Records that you have seen an alert.

It resolves nothing, approves nothing and sends nothing. A worker waiting on a
permission request is still waiting afterwards.

## `birddog watch add|remove|update`

Adjusts what an instance watches, without restarting it or disturbing anything
it is already watching.

```sh
birddog watch add    --instance ID --file target.json
birddog watch remove --instance ID --target worker-1
birddog watch update --instance ID --target worker-1 --expect-quiet-for 20m --reason "integration suite"
```

Every change is validated in full before any of it is applied, and advances a
configuration revision that events are stamped with — so a delayed observation
can be told apart from one made under the current rules. Changing thresholds
never rewrites history: the event feed is what was observed, not what the rules
were.

`--expect-quiet-for` suppresses quiet and idle alerts for a while and nothing
else. Exit, input requests and observation loss still surface, observation
continues and is still recorded, and when the override lapses the thresholds
start fresh rather than firing immediately about the silence you authorised.
`--clear-expected-quiet` ends it early.

Removing a target stops birddog watching it. It does nothing to the session,
and what was already observed of it is kept.

## `birddog stop --instance ID [--json]`

Stops an instance. Every watched session keeps running — birddog holds nothing
of theirs to release. The instance's database is kept.

## `birddog list [--json]`

Monitoring instances on this machine, and whether each is still running.
Whether an instance runs is decided by its process, never by the presence of a
record: a daemon that crashed leaves its record behind.

## `birddog hooks install|status|uninstall`

Lets birddog see tool calls and permission requests in Claude Code, which the
session registry does not carry.

```sh
birddog hooks install --dry-run    # show what would change
birddog hooks install
birddog hooks status
birddog hooks uninstall
```

**Per-user setup.** Hooks live in `~/.claude/settings.json`, so everyone using
birddog installs them on their own machine. Existing configuration is
preserved: birddog appends its handler, leaves other hooks in place and in
order, keeps a `.birddog-backup` of the previous file, and on uninstall
removes exactly its own entries.

**Sessions already running pick hooks up** — observed on Claude Code 2.1.267,
within minutes and without restarting. A new session is still the sure way.

`status` distinguishes a complete install from a partial one, because partial
coverage looks like coverage.

## `birddog hook`

The handler Claude Code runs. Not for interactive use — it reads one event on
stdin and records it.

It exits 0 unconditionally and writes nothing to stdout. Exit 2 from a
`PreToolUse` hook blocks the tool call, and stdout is added to the model's
context on several events; birddog does neither.

## `birddog prune [--dry-run] [--json]`

Forgets instances that are over: those stopped cleanly, and those whose
process is gone without having stopped. It says which of the two each was.

**It never stops a running instance.** An instance is still watching because
somebody asked it to, and nothing here knows whether they still care —
deciding that on its own is the one thing this must not do. `birddog stop`
remains the only way to end a live watch.

Recorded history is kept. A stopped instance can be resumed, and removing its
events would make that a lie.

## `birddog doctor [--json]`

What each adapter can and cannot observe on the versions installed here, which
alert routes this build offers, and the limitations that hold whatever is
installed. It reads versions and nothing else; no session is touched.

Every capability is stated twice. The sentence is for an operator reading a
terminal. Beside it, `--json` carries a `*_support` value a program can branch
on, because prose is free to be reworded by any commit that improves it:

| Value | Meaning |
|---|---|
| `supported` | Observable as installed, with nothing further to do |
| `requires_setup` | Observable once birddog's own instrumentation is in place — `hooks install`, or the opencode plugin |
| `partial` | The field bundles more than one thing and the answers differ; read the sentence |
| `provider_limited` | Not observable from outside for this provider, at any version — asking again will not help |

The distinction that matters to a caller is `provider_limited` against
`requires_setup`: the first says no fix exists, the second says one does. Both
read as "cannot see it" in prose.

These describe what the *installation* supports, not what this machine
currently has in place. `requires_setup` does not become `supported` once the
hooks are installed; it says what carries the capability, which is the thing an
operator acts on.

What this machine has in place is reported separately, by `instrumentation`:
one entry per mechanism, naming the provider it instruments so a consumer can
join it to the capability that says `requires_setup`.

| Value | Meaning |
|---|---|
| `not_installed` | Nothing is installed. A choice, not a fault, and it never carries a `problem` |
| `installed` | Installed, and nothing says it cannot work |
| `incomplete` | Installed for some of what it covers and not the rest — worse than absent, because it looks like coverage |
| `broken` | Installed and cannot work. The one value that carries a `problem`, and birddog's own fault rather than a provider's limit |
| `unknown` | Cannot be established from here, and reported as such rather than resolved to `not_installed` |

`unknown` occurs for the opencode plugin on a machine where nothing has been
published and the plugin has left no log: not installed and installed-but-never-run
are genuinely indistinguishable there, and birddog does not turn an absence into
evidence.

`version` on an instrumentation entry is the version last seen *running* — from
a publishing session where there is one, otherwise from the last recorded start.
It is not a claim about what is installed on disk.

`hook_handler_problem` keeps its name and meaning, and is now derived from the
`claude_hooks` entry so the two cannot disagree. It stays empty when no hooks
are installed, which is why it alone could never tell an absent installation
from a working one — that is what `state` is for.

`provider_limited` is the same word, with the same meaning, as the observation
reason of that name: one is stated about a provider, the other about a single
observation.

## Idempotency

Every mutation — `ack`, `watch add`, `watch remove`, `watch update` — accepts
`--idempotency-key`. Repeating a request with the same key returns the original
result instead of applying the change again, so a caller that did not hear the
answer can retry without adding a watch twice or clearing an override twice.

The record is durable, so a retry after birddog restarts is still answered from
the original. The first result wins: a retry is entitled to exactly what its
original request produced.

## Error codes

| Code | Meaning |
|---|---|
| `not_running` | No instance is listening on that socket |
| `not_found` | No such instance or incident |
| `unknown_method` | The control channel does not serve that method |
| `bad_request` | Malformed parameters |
| `cursor_stale` | Replay from that cursor would silently lose events |
| `store_replaced` | The cursor belongs to a store that no longer exists |
| `machine_mismatch` | The cursor belongs to a different machine, or to one this instance cannot confirm it is |
| `internal` | Something else went wrong; the message says what |
