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
NAME          PROVIDER  STATUS       PID    WHERE                               SESSION       CWD
api-refactor  claude    busy         41207  tmux muster:muster-bafad08e…:0.%38  11111111-…    /work/api
docs-pass     claude    idle         41377  Ghostty ttys002                     22222222-…    /work/docs
auth-thread   codex     unavailable  41880  ttys009                             01a0c46d-…    /work/auth
```

**Which registries.** Claude Code records a session under
`$CLAUDE_CONFIG_DIR/sessions`, so each profile's sessions are in that
profile's directory. birddog reads:

1. `~/.claude/sessions`, always — whether or not it exists yet;
2. `$CLAUDE_CONFIG_DIR/sessions`, when that is set in birddog's environment;
3. `<dir>/sessions` for each profile directory in `BIRDDOG_CLAUDE_CONFIG_DIRS`,
   a `:`-separated list (`~/` is expanded):

```sh
export BIRDDOG_CLAUDE_CONFIG_DIRS=~/.claude-work:~/.claude-home
```

Which directories are profiles is stated rather than guessed: a pattern such as
`~/.claude*` also matches backups and copies, and a copied registry would be
read as live sessions. A profile directory, or a registry, reached through a
symbolic link is skipped. The list is worked out again on every listing, so a
registry created after an instance started — a profile first used later — is
read from then on. A running instance, `--owner` and the `claude-inbox` route
read the same set, from the environment the instance was started with — and
only that one: setting `BIRDDOG_CLAUDE_CONFIG_DIRS` afterwards changes nothing
for an instance already running. `status --json` reports the list an instance
actually reads as `claude_registries`.

One process is one session. A process recorded in more than one registry is
identified by pid and start time together; the live record is kept, then one
that is not a link, then the newest. A record reached through a link belongs
to the directory it actually lives in, which is where its credential file is.

A registry that exists and cannot be read — or a record in one that cannot be
read or does not parse — makes the listing **partial**. A record is skipped
without saying so only on evidence that its session is over: its directory
entry is gone (the session ended while the directory was being read), or it is
a link to a missing record whose `<pid>.json` name gives a pid no process
holds. A link to a missing record for a pid that **is** running — a renamed or
unmounted directory, say — makes the listing partial, since the session may be
running behind it. When a listing is
partial, what was read is used, and a session missing from it is not
concluded gone — an owner is not counted absent, a target reports
`source_unreadable` rather than an exit, and a `claude-inbox` delivery is
retried rather than given up as having no recipient. `claude-inbox` chooses
between records with real liveness: a record at the orchestrator's process
that does not answer is not followed to. A registry that does not exist simply holds no sessions.

`--registry-dir DIR` narrows `discover` to that one directory.

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

### Where a session lives

Each live session with a pid — a Claude Code session, or the process holding
a Codex thread's lock — carries a `location` saying where to find it, so a
caller can focus its window or attach its pane:

```json
"location": {
  "tty": "/dev/ttys005",
  "terminal_app": "Ghostty",
  "tmux": {
    "socket": "/private/tmp/tmux-501/muster",
    "socket_name": "muster",
    "session": "muster-bafad08e-66ea-4330-b87f-a00feb6944c6",
    "window": "0",
    "pane": "%38",
    "target": "muster-bafad08e-66ea-4330-b87f-a00feb6944c6:0.0",
    "attached": false
  }
}
```

| Field | Meaning |
|---|---|
| `tty` | The session's controlling terminal. |
| `terminal_app` | The macOS app hosting that terminal — Ghostty, Terminal, iTerm2, Scotty — named after the nearest ancestor process that runs from an `.app` bundle. For a tmux pane it comes from an attached client, since the tmux server is a daemon; an unattached pane has none. **Best-effort**: omitted whenever it cannot be told, including over ssh. |
| `tmux.socket` | The tmux server's socket, absolute, for `tmux -S`. |
| `tmux.socket_name` | The `-L` name of that socket (`muster` for muster's sessions, `default` for plain `tmux`). |
| `tmux.session`, `window`, `pane` | The tmux session name, window index and pane id (`%38`; stable for the pane's life). |
| `tmux.target` | `session:window.pane-index`, ready for `tmux -t`. |
| `tmux.attached` | Whether any client is attached to that tmux session. |

`tmux` is filled in only when the session's process descends from a tmux
server, and only servers whose sockets sit in the default directory
(`$TMUX_TMPDIR`, or `/tmp`, then `tmux-<uid>`) are asked; a socket whose server
has gone is skipped. A pane on a server started with `-S` elsewhere shows only
its `tty`. `location` is absent for a session that is not live, or when nothing
could be found — locating never fails the listing.

The WHERE column condenses it: `tmux <socket_name>:<session>:<window>.<pane>`
(long session names cut short; the JSON has them whole), `<app> <tty>`, a bare
`<tty>`, or `—`.

birddog only lists: it runs `tmux list-panes` and `list-clients` and nothing
else, and never attaches or sends keys. To go to a session yourself:

```sh
tmux -S /private/tmp/tmux-501/muster attach -t 'muster-bafad08e-…:0.0'
tmux -L muster attach -t 'muster-bafad08e-…:0.0'   # same server, by name
```

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

`reason` is current even while `status` is history: a pass that observes
nothing keeps the last status and records why, ordered against observations
the same way they are ordered against each other, so a delayed report cannot
overwrite a newer one. A target that has never been observed reports its
`reason` with no `status`, no `observed_at` and `status_is_current: false` —
nothing it did not see.

### Policy

Every target carries `policy`: what it is being watched under **now**, in the
configuration file's own terms, so a `watch update` or `watch sync` can be
checked rather than trusted.

```json
"policy": {
  "alert_on": ["input_requested", "exit", "quiet", "observation_lost", "overdue"],
  "quiet_after_seconds": 300,
  "idle_grace_seconds": 30,
  "no_progress_after_seconds": 0,
  "expected_quiet_until": "2026-10-16T20:00:00Z",
  "expected_quiet_reason": "integration suite",
  "expected_quiet_active": true,
  "deadline": "2026-10-16T21:30:00Z",
  "deadline_passed": false
}
```

An instance started by a build before 2.1 does not report `policy`; the key is
absent and the text form says `unknown (instance predates 2.1)` rather than
reading zeroes as a policy. `birddog resume` it on 2.1 to see it. The same
instances answer `watch sync` with `unknown_method`.

`no_progress_after_seconds` is `0` when the condition is off, which is its
default. `expected_quiet_active` says whether the override is suppressing
quiet and idle at the moment of the snapshot; an override whose time has
passed is still shown, and is not active. The text form prints the same as a
second table, and says `nothing` for a target whose `alert_on` is empty — a
target that looks watched and alerts on nothing is what the table is there to
catch. `deadline` is absent when the target has none; a relative
`deadline_after_seconds` is reported resolved, as the time it falls due, and
the text form's `DEADLINE` column marks one that has gone by `(passed)`. The older top-level `expected_quiet_until` and `expected_quiet_reason`
on each target are kept and mean the same.

### Where a target lives

`attached_session_id` is the session id the target follows now — different
from the configured one once birddog has followed a `/clear`. `pid` is the
process last seen answering for it, and `location` is where that process lives,
in the same shape `discover` reports: tty, terminal app, and for a
tmux-hosted session the server socket and pane. `location` is present only
while the session is seen; one that has gone lives nowhere. The text form adds
a `WHERE` column.

### Its configuration file

`config_sync` names the file the instance was started with, whether it
follows it (`follow_file`), when a sync last succeeded, and `last_error` when
the file last changed and was refused. A refused file leaves the watch list as
it was. See `watch sync`.

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

### When a session changes id

Claude Code mints a new session id on `/clear` without restarting: the same
process, in the same terminal, carries on under an id nobody configured.
birddog follows it. When the id a target names has gone from the registry and
the **same process** — pid and start time together — now reports a different
live session, the target moves to the new id and the feed records a
`session_followed` event before the first observation of the new session:

```json
{"type": "session_followed", "target_id": "worker-1", "session_id": "22222222-…@Fri Oct 16 23:49:32 2026",
 "evidence": {"from_session_id": "11111111-…", "to_session_id": "22222222-…",
              "pid": 41207, "proc_start": "Fri Oct 16 23:49:32 2026", "via": "process"}}
```

It is not an exit and opens nothing. The new session is a new generation, so
anything still open about the old one is closed out, exactly as for a session
replaced at the same target id. `status` reports the id being followed.

The process identity comes from the target's `pid` and `proc_start` when the
attachment names them, and otherwise from the passes that saw the session
before its id changed. Nothing weaker is used: a pid alone is reused by the
kernel, and a terminal is reused by whatever runs in it next — either would
follow a target onto a stranger. A session resumed in a **new process** is a
new process and is not followed.

A process learned from earlier passes belongs to the target as it was then
defined. `watch add`, `watch remove`, and a `watch sync` that adds, changes or
removes a target all drop it, so a target added again — even for the same
session — starts fresh: continuity across that change comes only from a follow
already recorded in the store.

The configuration file is not rewritten. Follows are recorded in the
instance's store and replayed, so a resumed instance, or a `watch sync` from a
file that still names the original id, lands on the session the target had
been following rather than on an id that no longer exists.

A replayed follow gives way to the id that was configured. The configured
session is looked for first, and the follow applies only when it is not there
live — so a conversation resumed under its original id, in a new process, is
watched under that id rather than pulled back to the process that has gone. A
target whose attachment names a different `pid` and `proc_start` from the
process a follow was made in is left as configured. Following back to an id
(A → B, then B → A) retires the older link, so it cannot cycle.

`--owner` follows the owning session the same way, so an orchestrator that
runs `/clear` keeps its instance, and so does the `claude-inbox` notification
route, so alerts keep reaching the orchestrator under its new id. Each link is
the process found under the configured id — the owner's from the first pass,
the recipient's when the instance starts, before any alert — and is kept for
the life of the instance process. An instance started or resumed after that
session has already run `/clear` knows only the configured id, which no
longer exists: name the current id in the configuration.

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
| `overdue` | The target's deadline has passed and it has not exited |

**At most one incident is open per target, condition and generation**, and it
is delivered once while it stays open; repeated passes that still see the
condition only refresh it. A condition that ends and comes back is a new
incident, delivered again, in the same generation: a session that waits for
input, is answered and runs, then waits again raises `input_requested` twice.
A consumer deduplicating alerts should key on the incident id, not on the
target and generation.

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
birddog watch add    --instance ID --file target.json     # one target, in the file format
birddog watch remove --instance ID --target worker-1
birddog watch update --instance ID --target worker-1 --expect-quiet-for 20m --reason "integration suite"
birddog watch update --instance ID --target worker-1 --deadline 45m   # or --deadline 2026-10-17T09:00:00Z, --clear-deadline
```

`watch add` reads one target in exactly the format of an entry in a
configuration's `targets`, with the same defaults and validation. Before 2.1 its
`quiet_after_seconds`, `idle_grace_seconds` and `no_progress_after_seconds`
were lost on the way to the instance, and a target with no `alert_on` alerted on
nothing; `status` now shows each target's policy, so check one added by an
earlier build.

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

`--deadline` sets when the target is expected to have finished: a duration
from now (`45m`) or an RFC3339 time. If the target has not exited by then,
`overdue` opens — one incident, delivered once, not one per pass — and stays
open until the target exits or the deadline is moved ahead or cleared. It
fires whatever the session is doing, busy included, which is the case `quiet`
and `no_progress` are built not to report; an expected-quiet override does not
suppress it. `--clear-deadline` removes it. The target's `alert_on` must
include `overdue` (the default list does), or the deadline is refused rather
than set to alert on nothing.

In a file a deadline is `"deadline": "<RFC3339>"` or
`"deadline_after_seconds": N`, never both. The relative form runs from when the
instance took the target on — start, `watch add`, or the sync that added it,
including when a later sync introduces or changes it — so `resume` restarts it; use the absolute form for a deadline that must not
move. One run is one incident: a session that changes id (see *When a session
changes id*) after its deadline starts a new generation and raises its own.
An exit already recorded for the run is not undone by losing sight of the
session afterwards, so a target that finished in time is never reported
overdue. Dropping `overdue` from `alert_on`, by update or sync, resolves an
overdue incident that is open, since nothing would close it afterwards. An
update can drop it only once the target has no deadline: clear it first, or in
the same request (`--clear-deadline` alongside the new `alert_on` in `--file`).

Removing a target stops birddog watching it. It does nothing to the session,
and what was already observed of it is kept.

## `birddog watch sync --instance ID [--file FILE] [--dry-run] [--json]`

Makes an instance watch exactly what a configuration file says: targets the
file adds are added, targets it no longer lists are removed, and targets whose
definition changed are updated — so an orchestrator that keeps its watch list
as a file changes the file instead of issuing `watch add` and `remove` by hand.

```sh
birddog watch sync --instance ID                       # re-read the file it was started with
birddog watch sync --instance ID --file ledger.json    # match another file
birddog watch sync --instance ID --dry-run             # say what would change
```

The file is a whole instance configuration, the same format `start --config`
takes, and is validated in full first: one bad target refuses all of it and
nothing changes. One sync is one revision.

**An unchanged target is left exactly as it was** — its state, incidents and
history are kept, and restating it does not reset a threshold. A changed
target keeps its history too: everything birddog records is keyed by target
id. A removed target's history is kept, as with `watch remove`. A repeat sync
of the same file changes nothing and does not advance the revision.

The result lists every target as `added`, `removed`, `updated` or `unchanged`.
Retention is applied from the file. The instance's `name` and its
`notification` are fixed for the life of the process; a difference in either is
reported under `not_applied` and takes effect when the instance is resumed.

Two things a file does not override:

- **A runtime expected-quiet override.** One set with `watch update
  --expect-quiet-for` survives a sync from a file that does not mention one,
  until it lapses. A file that sets `expected_quiet_until` itself wins.
- **A deadline in force.** A relative deadline keeps the time it was resolved
  to while the file's `deadline_after_seconds` is unchanged, so re-syncing does
  not push it back; one moved or cleared with `watch update --deadline` stays
  that way while the file's deadline is what it was. A file that changes its
  deadline wins.
- **A followed session.** A file still naming the id a target was followed
  away from (see *When a session changes id*) means the session being
  followed.

`--file` does not change which file the instance was started with; `resume`
still reads that one.

### Following the file

Set `"follow_file": true` at the top of the configuration and the instance
re-reads its file whenever it changes, before the next pass, exactly as `watch
sync` would. A save that does not parse is refused and reported in `status` as
`config_sync.last_error`, and is not retried until the file changes again; the
watch list stays as it was, so a half-written save never empties it. It is off
by default.

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

All three take `--json`, and either `--config-dir DIR` or `--settings FILE`
(not both — that is a usage error, exit 2). `install` also takes `--binary
PATH` and `--dry-run`.

**Per-user, per-profile setup.** Hooks live in the settings file of the Claude
Code profile that runs the session, so everyone using birddog installs them on
their own machine, once per profile. The file is chosen in this order:

| Given | Settings file | `settings_source` |
|---|---|---|
| `--settings FILE` | `FILE` | `flag` |
| `--config-dir DIR` | `DIR/settings.json` | `config_dir_flag` |
| `CLAUDE_CONFIG_DIR` set and non-empty | `$CLAUDE_CONFIG_DIR/settings.json` | `CLAUDE_CONFIG_DIR` |
| otherwise | `~/.claude/settings.json` | `default` |

A leading `~` is expanded in each. A profile launched with its own
`CLAUDE_CONFIG_DIR` never reads `~/.claude/settings.json`, so hooks installed
there do nothing for it. To cover several profiles, install into each:

```sh
for p in work home; do
  birddog hooks install --config-dir ~/.claude-$p
done
```

`birddog doctor` resolves the file the same way, so its `claude_hooks` entry
describes the profile it was run under.

Existing configuration is preserved: birddog appends its handler, leaves
other hooks in place and in order, leaves every other key as it was (numbers
included, written back exactly), keeps a `.birddog-backup` of the previous
file, and on uninstall removes exactly its own entries. Running `install`
again on a file that is already correct reports `changed: false` and writes
nothing — no rewrite, no new backup.

**`install` checks the handler can run** and reports it as
`handler_runnable`, with `handler_problem` saying why when it cannot — a
missing binary, a directory, a file that is not executable. It warns and
installs anyway, since the binary may be about to be put in place; until it
is, every hook fires and fails.

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

Every mutation — `ack`, `watch add`, `watch remove`, `watch update`, `watch sync` — accepts
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
