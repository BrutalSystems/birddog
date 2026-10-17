---
name: birddog
description: "Use ONLY when asked to watch, monitor or keep an eye on coding-agent sessions (Claude Code, Codex, opencode) on this machine with the birddog CLI — \"keep an eye on the workers\", \"tell me if worker-1 stops or asks for input\", \"what are my sessions doing\", \"is that session still running\", \"alert me when it goes quiet\". Covers birddog discover, config files, start --owner, events, ack, stop and prune. Not for messaging a session or launching one."
---

# birddog — watching agent sessions

birddog observes live Claude Code, Codex and opencode sessions on this machine
and reports what it saw: busy, idle, waiting for input, exited, gone quiet. It
**only observes** — it sends nothing to a session, approves nothing, and never
starts, stops or restarts one. Stopping birddog leaves every session running.

Use it only when the user asks for watching. Never start an instance as a side
effect of messaging or launching a session; those are other tools' jobs.

Every command takes `--json`. Exit `1` is a failed operation, `2` a bad
invocation; errors print as `birddog: [code] message` (`not_found`,
`not_running`, `bad_request`, `cursor_stale`, `store_replaced`, …).
`birddog <cmd> --help` lists a command's flags; `docs/cli.md` in the repo is
the full reference.

## A quick look — no instance needed

```sh
birddog discover --json   # every attachable session: session_id, pid, proc_start, name, cwd, status
birddog doctor            # what each runtime can and cannot be observed for, here
```

**`discover` is empty, or misses sessions you know are live?** Claude Code
registers a session under `$CLAUDE_CONFIG_DIR/sessions`. birddog reads
`~/.claude/sessions`, `$CLAUDE_CONFIG_DIR/sessions`, and each profile listed in
`BIRDDOG_CLAUDE_CONFIG_DIRS` (`:`-separated, e.g.
`~/.claude-work:~/.claude-home`) — in `discover`, in a running instance, for
`--owner` and for the claude-inbox route. A session under a profile not in that
list cannot be watched: add the profile, in the environment the instance is
started from. `discover --registry-dir DIR` lists one directory.

A hand-started opencode TUI never appears: opencode is observable only with the
`@brutalsystems/birddog-opencode` plugin loaded. Codex threads show
`unavailable` — expected, see below.

## Watching over time

1. **Write a config** (JSON, `schema_version: 1`), one target per session,
   from `discover --json`. Validation is strict: an unknown field fails the
   start. Put it in a scratch location, not the user's repo.

   ```json
   {
     "schema_version": 1,
     "name": "checkout-refactor",
     "targets": [{
       "id": "worker-1",
       "provider": "claude",
       "attachment": {"kind": "existing-session", "session_id": "…",
                      "pid": 41207, "proc_start": "Mon Sep 21 15:53:11 2026"},
       "policy": {"alert_on": ["input_requested", "exit", "quiet", "observation_lost"]}
     }]
   }
   ```

   - For Claude Code, copy `pid` **and** `proc_start` beside `session_id`;
     without them birddog cannot tell a finished session from one it cannot
     see. Codex takes the thread id as `session_id` only.
   - `provider` is `claude`, `codex`, `opencode` or `process`.
   - Defaults: `alert_on` = the four above, `quiet_after_seconds` 300,
     `idle_grace_seconds` 30. `idle` is opt-in and noisy (every Claude Code
     turn ends idle).
   - `no_progress` needs **both** the name in `alert_on` and
     `no_progress_after_seconds`; there is no default. Without hooks it
     measures turn length, not silence — set it above the longest turn.
   - `"observations": {"transcript": true}` lets a Claude Code target detect a
     question asked in plain prose (which otherwise looks idle).
   - Optional `notification`: `{"kind": "claude-inbox", "options":
     {"session_id": "<orchestrator>"}}` delivers alerts to a Claude Code
     session at its next turn boundary; `{"kind": "tincan", "options":
     {"peer": "<name>"}}` needs tincan 2.0.0+; omitted or `none` means poll.
   - The repo's `examples/` has `claude.json`, `codex.json`,
     `with-alerts.json`, `expected-quiet.json`, `with-retention.json`.

2. **Start it owned by this session**, so it shuts down when this session ends:

   ```sh
   birddog start --config watch.json --owner "$CLAUDE_CODE_SESSION_ID" --json
   ```

   The owner must be a Claude Code session `discover` lists. If
   `CLAUDE_CODE_SESSION_ID` is unset, find this session's row in
   `discover --json` (its `pid` is an ancestor of your shell). **Without
   `--owner` the instance outlives everything** and nothing reaps it — leave
   it off only when the user wants a watch that spans conversations. The
   result carries `instance_id` and the starting `cursor`.

3. **Follow it**:

   ```sh
   birddog events --instance <id> --after <cursor> --wait 25 --json
   birddog status --instance <id> --json     # every target now, open incidents, cursor
   ```

   - `--wait` is capped at **25 s**; longer is clamped (`wait_clamped`). An
     empty page after the wait is an answer, not a failure. Loop, passing back
     the returned `cursor` (and `--store <store>` once you have one).
   - `cursor_stale` → take a fresh `status` and continue from its cursor; the
     gap really happened. `store_replaced` → birddog's state was reset; start
     from `status` again.

4. **Report each alert to the user**, then acknowledge it:

   ```sh
   birddog ack --instance <id> --incident <n>
   ```

   Incident ids come from `status` (`targets[].incidents[].id`), not from the
   event feed. `ack` only records that the alert was seen — a worker waiting on
   a permission prompt is still waiting.

5. **Adjust without restarting**: `birddog watch add --instance <id> --file
   target.json`, `watch remove --target worker-1`, `watch update --target
   worker-1 --expect-quiet-for 20m --reason "test suite"` (silences quiet/idle
   only; exit, input and loss still surface). If the watch list lives in a
   file, edit the file and run `birddog watch sync --instance <id>` (or
   `--file other.json`; `--dry-run` first): it adds, removes and updates to
   match, leaving unchanged targets' history alone. `"follow_file": true` in
   the config makes the instance do that by itself on every save. Verify a
   change with `status --json`: each target carries its `policy`.

6. **Stop it when the watching is done**: `birddog stop --instance <id>`.
   `birddog list` shows instances (`RUNNING` is decided by the process, not
   the record); `birddog prune` forgets ones already over and never stops a
   live one; `birddog resume --instance <id>` restarts a stopped one with its
   history intact.

## Reading the answers

- **`unavailable` / `input_request_visibility: unavailable` never means
  "unblocked".** It means birddog cannot see. Codex threads are always
  `unavailable`; Claude Code needs hooks for tool calls and permission prompts.
- **`(stale)` / `status_is_current: false`** — the last thing observed, not a
  present fact. Re-run `discover`; a restarted session is a new run.
- **`session_followed`** in the feed — a Claude Code session ran `/clear` and
  got a new id in the same process; the target moved to it (old id → new id in
  the evidence). Not an exit. A resume in a new process is not followed.
- **Where a session lives**: `discover --json` and `status --json` carry
  `location` — `tty`, `terminal_app`, and for tmux (`muster` uses `tmux -L
  muster`) the socket, session and pane, so it can be focused or attached.
- **A Claude Code question asked in prose reports `idle`**, identical to a
  finished turn, unless the target has `transcript: true`. A turn ending is
  not the work finishing.
- **`quiet` never fires while a session reports it is busy** — by design. A
  session wedged while "busy" needs `no_progress`.
- **After a verified exit the target's feed goes silent**; that silence is not
  birddog dying. Liveness of the instance is `status` answering.
- `observation_lost` is lost contact, never an exit. `correlated_loss` (every
  target lost at once) is one fault on birddog's side, not N dead sessions.
- `start` fails to become reachable → read the daemon log at
  `~/Library/Application Support/birddog/logs/<instance>.log`.

## Hooks — ask first

`birddog hooks install` lets birddog see tool calls and permission requests in
Claude Code. It edits the settings file of one Claude Code profile —
`$CLAUDE_CONFIG_DIR/settings.json` when that is set, else
`~/.claude/settings.json`, or `--config-dir DIR` to name another — preserving
existing hooks and keeping a `.birddog-backup`. That affects every session
running under that profile, and no other: a user with several profiles needs
it once per profile. **Ask before running it**; show
`birddog hooks install --dry-run` first — its `settings_path` says which file,
and `handler_runnable: false` with a `handler_problem` means the hooks would
fire and fail. `birddog hooks status` reports a
partial install, which is worse than none. `birddog hooks uninstall` removes
exactly its own entries.

## Don't

- Use birddog to message, prompt, approve or launch a session. It cannot.
- Stop or prune an instance you did not start — list it for the user instead.
- Leave an instance you started running after the watching is done.
- Treat an empty feed, `unavailable`, or `idle` as evidence a worker is fine.
