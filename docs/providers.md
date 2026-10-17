# Provider capabilities

What each runtime exposes to an **external** observer, and what it does not.
Every row was established by inspecting running programs rather than by reading
vendor documentation — twice now, vendor docs have been wrong about exactly
these details.

Capabilities are pinned to versions and are not claimed beyond them. The raw
probe record is kept out of this repository; what it established is here.

| Verified against | |
|---|---|
| Claude Code | 2.1.267, and 2.1.274 for the findings dated 2026-09-26 |
| Codex CLI | 0.155.1 |
| opencode | 1.18.31 |

## Summary

| | Attach to running session | Runtime state | Tool start/stop | Input/permission waits | Session→process mapping |
|---|---|---|---|---|---|
| **Claude Code** (terminal) | **Yes** — registry, no mutation | **Yes**, with freshness | **With hooks installed** | **Yes** — the registry reports a waiting session; hooks add which tool | **Yes** — pid + start time + session id |
| **Codex** (terminal) | Partial — via own app-server child | **Mostly no** — see `notLoaded` below | **No** | **No** | **Yes** — writer lock holder |
| **opencode** (launched with plugin) | **Yes** | **Yes** — active, idle, running_tool, waiting_input | **Yes** | **Yes** — the only provider that can | **Yes** — pid + heartbeat |
| **opencode** (hand-started TUI) | **No** — nothing external can find it | — | — | — | — |
| **Desktop apps** | Out of scope for v1 ([D3](./decisions.md#d3--desktop-apps-are-out-of-scope-for-v1)) | — | — | — | — |

A blank cell is a real gap. An adapter must report it as an honest capability
limitation rather than degrade quietly into a guess.

Note where the best coverage is: an instrumented opencode session, because a
plugin inside the process sees what no outside observer can. Claude Code's
registry also reports a waiting session without any instrumentation (below).
Codex remains `unavailable`, which is not the same as reporting that there is
none.

## Claude Code

The harness maintains a live session registry at `~/.claude/sessions/`, one
JSON record per running session. Reading it mutates nothing — no hooks, no
configuration edit, no restart — which is what makes attaching to sessions that
were *already running* possible at all.

Each record carries the session id, working directory, process id and process
start time, a name, the runtime status with its own timestamp, the kind of
session and its entrypoint, the harness version, and the path of the session's
unix socket.

That supplies, without hooks: attachment, runtime state, separate
`last_contact`/`last_activity` freshness, the PID-reuse guard, and the
terminal-vs-programmatic distinction.

### A waiting session is visible without hooks [verified]

**A session blocked on a permission prompt reports `status: "waiting"` and
carries a `waitingFor` field.** Both appear when the prompt goes up and both
are gone when it is answered — `waitingFor` is removed from the record
entirely, not blanked.

Established 2026-09-22 against Claude Code 2.1.267 by driving a real session
into a permission prompt in a throwaway workspace and sampling its registry
record twice a second throughout. The observed transition was
`idle` → `busy` → `waiting`, and `waitingFor` read the plain string
`"permission prompt"` — a description, not a payload: no command, no path, no
diff.

This is why Claude Code reports `input_request_visibility` as `observed` or
`not_observed` with **no hooks installed**. Earlier drafts of these docs said
the opposite, on the reasonable assumption that only hooks could carry it.
That was never tested; it is now.

Two limits, both real:

- **`waiting` covers the permission prompt, and the question *menu*.** Driving
  a real session on 2026-09-26 against Claude Code 2.1.274: a session blocked
  on an `AskUserQuestion` menu reports `waiting` with
  `waitingFor: "input needed"`, so the registry does see that one.
  What it does not see is a question asked in **plain prose** — the session
  reports `idle`, indistinguishable from one whose turn simply ended.
  Transcript reading is what separates those two; see "Questions, read from
  the transcript" below.
- **A session at the folder-trust prompt has no registry record at all.** It
  is created after trust is granted, so a session blocked on trust is
  invisible to birddog rather than reported as waiting.

Values seen in the binary but not explained — `compacting`, `requesting`,
`tool_use`, `needs_input` — are still refused rather than guessed at. `waiting`
left that list by being observed, which is the only way anything leaves it.

**Liveness needs two tests, not one.** A record can outlive the process that
wrote it, so the socket is connect-probed; and a PID can be reused, so the
process start time is compared against the record. Either failing means the
session is gone — and the replacement process is never adopted.

### Questions, read from the transcript

A question reaches a human two ways, and the registry sees only one of them.

Driven against a real session on 2026-09-26, Claude Code 2.1.274:

| The session asks | Registry `status` | `waitingFor` | Seen without a transcript? |
|---|---|---|---|
| An `AskUserQuestion` menu | `waiting` | `"input needed"` | **Yes** — and hooks add `AskUserQuestion` as the subject |
| Plain prose ending in `?` | `idle` | absent | **No** |

The second row is the gap. Measured across six live sessions the same day:
three were idle, one of them blocked on a prose question, and the registry
said `idle` about all three.

Because the registry already answers the first row, the transcript reader
defers to it: an input request that is already `observed` is never overwritten
by one read out of a conversation. So in practice the exact
`AskUserQuestion` signal is a backstop, and `question_inferred` is what this
feature adds.

The session transcript separates them. One file per session at
`~/.claude-<profile>/projects/<cwd-slug>/<sessionID>.jsonl`, read never
written, opted into per target:

```json
"observations": { "transcript": true }
```

Off by default: it is work on every pass, and one of its two signals is a
heuristic an operator should choose rather than inherit.

Two shapes, which are **not** equally reliable and are reported apart:

| Detected | `input_request_kind` | `evidence` | Reliability |
|---|---|---|---|
| An `AskUserQuestion` call with no matching `tool_result` | `question` | `transcript` | Exact — the turn cannot proceed until a human answers. Reached only where the registry did not already report the menu, so it is a backstop rather than the common path |
| The trailing assistant message ends in `?` | `question_inferred` | `transcript` | A heuristic; a rhetorical closing question false-positives. This is the case nothing else sees |

`question_inferred` fires only when the registry already says `idle`. Every
busy session sampled ended on a tool call, so a trailing `?` under `busy` is
prose mid-turn rather than a question awaiting an answer.

Either sets `waiting_input` and raises the existing `input_requested`
condition. The new `evidence` field is what tells a consumer how the answer
was reached — `registry`, `hook` or `transcript` — so an orchestrator can act
on an exact signal and merely surface an inferred one.

Two limits:

- **It needs a transcript.** A session at the folder-trust prompt has neither
  a registry record nor a transcript, so it stays invisible.
- **Only Claude Code.** The mechanism is provider-agnostic and the reader is
  one file per provider, but only the Claude Code reader ships.

### Hooks, for what the registry cannot carry

The registry says whether a session is busy, idle or waiting. It does not say
which tool is running, or which tool a waiting session is waiting on. Hooks
supply those:

| Event | What birddog learns |
|---|---|
| `PreToolUse` / `PostToolUse` | a tool is running, and its name |
| `PermissionRequest` / `PermissionDenied` | a request is outstanding, or answered |
| `Stop` / `SubagentStop` | a turn ended — and nothing more than that |
| `SessionEnd` | the session is gone; its state is discarded |

Hooks still add what the registry cannot carry: **which tool** is waiting, and
tool start and stop. The registry says a session is waiting; hooks say what for.

**This is per-user setup, not something the repository can do.** Hooks live in
an operator's own `~/.claude/settings.json`, so every person using birddog
installs them on their own machine:

```sh
birddog hooks install      # --dry-run first if you like
birddog hooks status
birddog hooks uninstall    # removes birddog's entries, nothing else
```

Existing configuration is preserved: birddog appends its handler, leaves other
hooks in place and in order, and uninstall removes exactly its own.

**Install a path that does not move.** A hook entry names an absolute path, and
`hooks install` writes the binary it is running from. Under a version manager
that is a path with a version in it — `~/.asdf/installs/nodejs/<version>/bin/birddog`
and its nvm, volta, fnm and nodenv equivalents. Upgrading birddog rewrites the
same path and is fine; upgrading *node* moves the directory and leaves every
hook naming a binary that is no longer there, still registered and silently
failing on every tool call.

birddog warns when the path it is about to write looks like that, and names the
escape hatch:

```sh
birddog hooks install --binary ~/.asdf/shims/birddog
```

It does not choose a different path on its own. Resolving `birddog` on PATH
would pick whatever comes first, which may be an older binary somebody built by
hand — the same failure by a shorter route. `hooks status` reports the risk
again for an installation that already exists, and reports outright when the
registered handler can no longer be run.

**Sessions already running do pick hooks up. [verified]** The handoff asked
that this be determined rather than assumed, and it was: on Claude Code
2.1.267, sessions started well before `birddog hooks install` began firing
hooks within minutes of it, without restarting. Earlier drafts of these docs
claimed a restart was required — that was wrong.

A new session remains the sure way, since nothing here is a documented
guarantee. `doctor` still reports what the *installation* supports rather than
what any particular session does.

**The handler cannot block anything.** A `PreToolUse` hook exiting 2 blocks
the tool call, and any other nonzero exit shows the operator an error notice,
so `birddog hook` exits 0 unconditionally — on a malformed event, on an
unwritable state directory, on anything. It writes nothing to stdout either,
because Claude Code adds a hook's stdout to the model's context on several
events and birddog has no business putting anything there.

The state it records is lock-free by design. Hooks fire as separate short
processes and Claude runs tools in parallel, so two handlers can be writing at
once; each event touches only its own marker file and state is derived by
listing a directory, so nothing has to read-modify-write and no handler ever
waits on a lock. Markers expire, because a session killed mid-tool leaves one
behind and a marker is not evidence of a tool still running two hours later.

A `Stop` event ends a turn. It does not prove the work finished, and birddog
never reports it as if it did.

### Two traps

**The registry directory also holds credential files.** Alongside each
`<pid>.json` record sits a `<pid>.<hash>.key` file carrying a peer token. It is
valid JSON, so a listing that globs `*.json` both invents phantom sessions and
reads credential material it has no business touching. Match `<pid>.json`
exactly, and assert in a test that no token reaches any output.

**`procStart` is UTC; `ps -o lstart` is local.** The registry records the
process start time in UTC, while the obvious way to read it back prints in the
caller's zone and pads with trailing spaces. Compared unconverted, every live
session fails its identity check and looks like PID reuse — a four-hour error
in US/Eastern, a date rollover in Asia/Tokyo. Pin the child's environment to
`TZ=UTC` rather than trusting the caller's.

## Codex

**The App Server is not the attachment route.** On 0.155.1 there is no
app-server control socket, and `thread/loaded/list` reports only threads loaded
in the *calling* process — useless from outside.

What works instead:

- **Enumeration and state** — `thread/list` over a `codex app-server --listen
  stdio://` child, spoken JSONL. `initialize` must declare
  `experimentalApi: true` or the daemon withholds the methods. Yields name,
  cwd, status and `canAcceptDirectInput`.
- **Liveness** — a thread is live exactly when a running process holds its
  writer lock in `~/.codex/thread-writer-locks/`.

**This costs a process.** birddog must spawn and hold its own `codex
app-server` child to read shared state. That is not "attaching to" a session,
and it is one long-lived child per birddog instance. Budget for it.

The one IPC socket that exists under `~/.codex/` belongs to the Codex **desktop**
application, not the CLI, and the app-server daemon lock files are stale with no
holder. Desktop is out of scope for v1, so this is not a route.

### `notLoaded` is birddog's own view, not the session's

The state database reports a thread's status as `notLoaded` when it is not
loaded in the **querying** app-server. Since every live thread is owned by
somebody else's process, that is what nearly every live thread reports. It says
nothing about what the session is doing.

Treating it as a session state is the failure acceptance criterion 6 names: an
adapter's own view leaking through as the observed subject's. tincan maps it to
`idle`, which is correct for a messaging tool deciding whether a send would
interrupt someone; for an observer it would be inventing an observation.
birddog reports the state as unknown, keeps the thread in the listing, and
relies on the writer lock for the liveness it genuinely does know.

The practical consequence: **Codex runtime state is usually unavailable.** What
is reliably known is that the thread exists, is live, who holds it, and where
it is running.

Tool events and permission waits are not observable for Codex from outside.

## opencode

**A default opencode TUI opens no TCP port at all.** It runs its server in a
worker thread behind a nominal base URL with an in-process fetch bridge. There
is no port file, lockfile, PID file or environment variable. Nothing outside
the process can find it. Published documentation claiming the TUI assigns a
random port is stale.

So observation requires code running *inside* the session: a plugin. birddog
does not install it — [muster](https://github.com/BrutalSystems/muster) does, at
launch, from an npm specifier. Instrumentation is the launcher's act, which is
what keeps birddog passive ([D2](./decisions.md#d2--opencode-is-in-scope-instrumented-at-launch-by-muster)).

```toml
[plugins.birddog]
npm = "@brutalsystems/birddog-opencode"
```

A session launched without the plugin is not observable, and the adapter says
so rather than reporting silence.

### What being inside buys

The plugin subscribes to opencode's event stream and its tool hooks, so it
sees more than any outside adapter does:

| Reported | From |
|---|---|
| `active` / `idle` | `session.status`, `session.idle` |
| `running_tool`, with the tool's name | `tool.execute.before` / `.after` |
| `waiting_input`, with the request's id | `permission.asked` / `permission.replied` |
| identity, directory, opencode version | `session.created` / `.updated` |

`permission.asked` is why opencode is the only provider whose input-request
visibility is ever anything but `unavailable`.

**All three paths are now [verified]** against a running opencode 1.18.31
session: the record's shape, `running_tool` caught mid-call during a slow
`bash`, and a permission prompt observed live as `waiting_input`, resolving
back once approved.

The permission payload was **not** what the documented event list implied, and
the difference only showed up under a real prompt. `permission.asked` carries
the request id at the top level as `id`; `permission` is the *kind* being
asked for — a plain string like `"bash"` or `"edit"` — not an object holding
the id. `permission.replied` refers back with `requestID`, not a nested id.

The first implementation guessed an object and its tests passed against the
guess, while a live session reported every request as `unknown`. The reader's
leniency is what kept that a degradation instead of a crash; it is not what
made it correct. The fixtures are now the captured payloads.

The request's subject is carried too, bounded to 200 characters: an alert that
says `bash: rm test.tst` is worth considerably more than one saying a request
is outstanding. An edit request's metadata also contains the full diff, which
is deliberately not stored — birddog records evidence, not payloads.

**The plugin must never throw.** opencode's `tool.execute.before` can *block a
tool* by throwing — that is how its own env-protection example works — so an
exception escaping birddog would turn a monitoring failure into the agent's
failure. Every hook is guarded, and a test asserts each one swallows a
publishing failure.

Liveness needs two things again: the publishing process alive, and a fresh
heartbeat in the record. Either alone is not enough, because a record outlives
its writer and a pid outlives its holder.

### Three traps

1. **Entry point.** The opencode loader reads `exports["./server"]` and falls
   back to `main`; it **does not read `exports["."]`**. A package declaring only
   `.` is fetched, its `package.json` is read, the session runs — and the plugin
   never executes, with nothing logged. The release smoke test resolves through
   `./server` for exactly this reason.
2. **Version drift is invisible, by either route.** Record a `plugin_version`
   in every registry record and compare it against what is installed —
   nothing else surfaces the difference. A copied file goes stale silently,
   and so does an npm specifier: opencode resolves it once into
   `~/.cache/opencode/packages/<pkg>@latest` and never revisits, so
   publishing a new version changes nothing until that directory is removed.
   Verified — a release sat on the registry while muster kept loading the
   version before it.
3. **Selecting a plugin relaxes isolation.** opencode merges project-local
   `.opencode/plugin` discovery into any non-empty plugin list, so an
   instrumented child also loads whatever plugins the target repository ships.
   Select birddog only for repositories trusted with that.
4. **Every export is invoked as a plugin**, and the loader does not descend
   into subdirectories. Export exactly one thing and never a `default`; a
   second export becomes a second plugin. A test and the release smoke test
   both assert this.

## Notifying the orchestrator

birddog's alert recipient is the orchestrator, not a watched worker.

[tincan](https://github.com/BrutalSystems/tincan) reaches all three runtimes —
Codex, Claude Code and opencode — through one adapter each, and has since
1.4.0. An earlier draft of this section said it declined Claude Code and was
therefore unusable here; that was true of the tincan of 2026-09 and is not
true now. It is a stdio MCP server with no `send` subcommand today, so calling
it from birddog waits on that CLI existing.

The route for a Claude Code orchestrator is its inbox socket, authenticated
with the peer token from its registry record — which means implementing that
delivery path rather than calling tincan. tincan's adapter is the reference to
read.

Delivery semantics worth matching, whichever route is used: fire-and-forget
returning when the harness accepts the message, a stable message id, a log
recording delivered/held/dropped, and queueing to a busy peer rather than
interrupting it.

Whatever the transport, every attention event stays available through birddog's
own event feed. Notification failure must never affect observation.

### The `tincan` connector

For an orchestrator that is **not** a Claude Code session. `claude-inbox`
speaks one runtime's inbox socket directly; tincan reaches Codex and opencode
as well, through one adapter each, so birddog does not implement three wire
formats to alert three kinds of orchestrator.

```json
"notification": {"kind": "tincan", "options": {"peer": "orchestrator"}}
```

`peer` is the orchestrator's name as tincan lists it. `binary` optionally names
the executable; otherwise `tincan` is resolved on PATH.

**It requires tincan 2.0.0 or newer, and probes at startup.** tincan is
installed separately and birddog cannot version-pin it — it can be upgraded or
downgraded under a running birddog, so the check is a runtime one. An operator
who configured this route is entitled to be told at startup that it cannot
work, rather than discovering it when the first worker blocks.

| Installed tincan | `tincan send --help` | birddog |
|---|---|---|
| 2.0.0 or newer | exit 0 | starts |
| every older build | non-zero | refuses to start, naming the upgrade |

tincan documents that exit code as its capability check, which is why the
probe reads nothing else. It used to match the string `unrecognised argument`
on stderr, because 1.9.2 and 1.10.1 both exited non-zero from a bare `send`
and only the wording told them apart — and that wording was tincan's usage
prose, so a reword would have silently turned the probe into one that accepts
a tincan that cannot deliver.

Refusing 1.10.1 is deliberate, and not only about the probe. It has `send` and
delivers, but predates `duplicate_peer_moved`; the outcome table below reports
`duplicate_send` as `delivered`, which is only true where that other refusal
exists to carry the case the name moved.

**What each answer is taken to mean.** tincan's `accepted` and birddog's
`delivered` make the same claim — the destination accepted it — and neither
means the session read it. Nothing here upgrades that.

| tincan says | birddog records | why |
|---|---|---|
| `accepted` | `delivered` | the harness transport took it |
| `rejected` / `duplicate_send` | `delivered` | this alert already went out, and the peer being addressed has it — see below |
| `rejected` / `duplicate_peer_moved` | `delivery_unknown` | it went out, but the name has since moved to a different session, which never saw it — see below |
| `rejected` / `key_reused` | `failed` | birddog's own key collided and nothing was sent — a birddog bug, reported rather than hidden |
| `rejected` / `peer_unknown`, `peer_ambiguous`, `peer_unreachable`, `peer_changed`, `self_send` | `no_recipient` | resolves identically next time, so retrying spends the budget on a configuration error |
| `failed` | `failed` | attempted, the transport did not take it |
| exit 64 | `failed` | birddog built the command line wrongly; nothing arrived |
| no result line, or a refusal this build does not know | `delivery_unknown` | nothing was established, and guessing would claim otherwise |
| timed out | `delivery_unknown` | a connector that can hang stalls the observation pass behind it, so the send is bounded and the answer is honest |

**Why the two duplicates are recorded differently.** tincan decides "same
send" by comparing the peer name as typed and the message text, not the
durable id of the session that received it. Within its ten-minute idempotency
window a peer name can move to a new session — session display names on this
machine demonstrably move within a day under fixed ids — and then the session
now holding that name never saw the alert.

From 2.0.0 tincan separates those cases itself. `duplicate_send` means the
peer being addressed really does have the message, which is the same claim
`accepted` makes, so birddog records `delivered` and the ordinary retry path
is restored. `duplicate_peer_moved` is the one where the name moved, and there
nothing was established: `delivered` would tell an orchestrator a worker had
been notified of something it may never have been told, which is the one claim
birddog exists not to make, and `failed` would invite a retry that resolves
identically. `delivery_unknown` stops further attempts and leaves the recovery
to the event feed.

Against an older tincan both arrived as one refusal and birddog had to record
`delivery_unknown` for either, which is why the connector refuses to start on
one. In practice this is reached rarely — birddog retries only after a
`failed` attempt, and tincan does not burn a key on a send that never landed —
but rarely is not never, and the cost of being wrong is a false negative on a
blocked worker.

Alerts carry `--reply-via birddog ack --instance <id> --incident <n>`, because
an orchestrator replying down this channel would be answering a one-shot
process that has already exited.

### The `claude-inbox` connector

birddog delivers to a Claude Code orchestrator directly, over that session's
inbox socket — the route tincan uses internally and does not expose. It is
configured by naming the session:

```json
"notification": {"kind": "claude-inbox", "options": {"session_id": "…"}}
```

Written against the wire format of Claude Code 2.1.267, which is not a
published interface and may change. An auth frame carrying the session's peer
token, then a user frame at `priority: "next"` — queued for the turn boundary,
so birddog never interrupts the orchestrator it reports to. The peer token is
read here and nowhere else in birddog; discovery deliberately refuses to touch
those files.

The recipient is resolved for every delivery, not once at startup: a session
that restarts gets a new pid, socket and token, and a recipient captured early
would point at a session that no longer exists.

**This route has no positive acknowledgement.** The session replies only to
report a *hold*, so silence is how it accepts. Treating silence as uncertain
was the first design here and live delivery disproved it — the alert arrived
and nothing came back, which would have recorded every successful delivery as
`delivery_unknown` forever, never marking anything notified and never retrying
anything either. Delivery is therefore established by the write going out and
the connection staying healthy through a short settle window; a connection
that breaks in that window is the genuinely uncertain case.

Adding another route — Codex, opencode — is a new package and one import:
connectors register themselves, and nothing in the monitor, the daemon or the
commands changes. Each must answer the same three questions, and the third
decides whether it should exist at all: who is the recipient, what did the
attempt establish, and can it reach a busy orchestrator without interrupting
it. Note that a Codex connector would need `experimentalApi` for
`thread/queue/add` — which the *observation* client deliberately refuses, and
must keep refusing. They are separate connections for separate purposes.

`"kind": "none"` remains the default: alerts are polled with `birddog events`,
which is the recovery path under every route and the whole answer under none.

The three outcomes matter more than the transport. *Delivered* is never resent
— re-announcing an unchanged condition is the storm incidents exist to prevent.
*Failed* is retried within a bound, because nothing arrived and a retry cannot
duplicate. *Uncertain* is neither: the attempt returned without establishing
anything, and an asynchronous call returning promptly is not an
acknowledgement. Resending risks a duplicate the destination cannot
deduplicate, and reaching for a second transport risks the same alert arriving
twice by two routes — so birddog records `delivery_unknown`, stops, and leaves
the feed to carry it. Exactly-once cannot be promised without the destination's
help, so it is not promised.

## Generic process observation

Where no adapter can see inside a session, a target can be registered as a
process instead: a pid plus its start time, and the files its work touches.

It establishes three things and claims nothing more. The process is the one
that was registered — a pid whose start time no longer matches is a verified
exit, never a replacement quietly adopted. A running descendant means work is
happening: a session blocked on a twenty-minute compile is indistinguishable
from a stopped one without this, and its own CPU being idle proves nothing.
And a named file growing, shrinking or being replaced is activity.

Watched paths are named explicitly — no recursive watch, no whole-home scan.
Truncation and in-place rotation are read as change rather than as silence,
because a rotated log can keep nearly the same timestamp while its content is
gone. A path that cannot be read is reported unavailable, never as a quiet
worker: "I cannot look" and "nothing happened" are different answers.

With a live process and no descendants, birddog does not know what it is doing,
and says so. Calling that idle would be a claim.

## Platform notes

**birddog does not build on Linux**, and does not pretend to. Its process
identity, paths and file-identity packages are `_darwin.go` files, so a Linux
build fails at "build constraints exclude all Go files" rather than compiling
into something untested. The release workflow verifies the Go side on macOS
for exactly this reason: running it on a Linux runner tested nothing, it just
failed to build.

**macOS caps unix socket paths at 104 bytes** (`sockaddr_un.sun_path`), and
`bind` fails past it with the unhelpful `invalid argument`. A path under
`t.TempDir()` exceeds it on its own.

**macOS does not advance the monotonic clock while asleep.** That is how a
sleep is detected: wall-clock time passing that monotonic time did not. Without
noticing, a laptop closed overnight wakes to find every target silent for eight
hours and reports a storm about time nobody was watching. On waking, quiet and
idle thresholds restart from the wake; exit and input-request conditions are
facts rather than durations and still surface immediately.

This is a design constraint, not just a test annoyance. Durable state belongs in
`~/Library/Application Support/birddog`, but an instance socket cannot live
beside it: measured on a machine with a short account name, that path plus an
instance id comes to **107 bytes** — over the limit by three. So birddog splits
the two. The database sits in application support; sockets go in a short
per-uid directory under `/tmp` (46 bytes for the same instance), which is where
Claude Code puts its own for the same reason. Instance ids are bounded so the
socket path cannot grow past the limit whatever the caller passes.
