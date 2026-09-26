# Acceptance criteria

The twenty-one criteria from [handoff.md](./handoff.md), and where each is
demonstrated. "Live" means verified against real sessions on a developer
machine; "smoke" means [`scripts/smoke.sh`](../scripts/smoke.sh), which runs in
CI.

| # | Criterion | State | Where |
|---|---|---|---|
| 1 | Two instances, stable ids, JSON results | **done** | live; `daemon` tests |
| 2 | Monitoring survives the starting shell | **done** | live — the daemon's parent becomes pid 1 |
| 3 | Idle reported without claiming completion | **done** | `policy`, smoke |
| 4 | Input-request alert; ack approves nothing | **done** | smoke; `store`, `daemon` tests |
| 5 | Long tool run is not a stall | **done** | `policy`, `observe`, smoke |
| 6 | Heartbeats do not advance session activity | **done** | `observe` — `notLoaded` refused, session's own timestamps used |
| 7 | Exit and PID reuse distinguished | **done** | live; `proc`, `observe` tests |
| 8 | Lost telemetry is unknown/stale, not invented | **done** | smoke, `monitor` |
| 9 | Rotation, truncation, unreadable paths, spaces | **done** | `observe` file tests |
| 10 | Sleep/wake does not produce false quiet | **done** | `wake`, `policy`, `monitor` |
| 11 | Cursor replay across restart | **done** | `store`, `daemon`, smoke |
| 12 | Zero prompts; workers untouched by stopping | **done** | live; smoke; control channel has no such method |
| 13 | Unsupported attachment reports a limitation | **done** | `doctor`, `providers.md` |
| 14 | Absence of a request is never "unblocked" | **done** | every surface reports visibility; only opencode can report `observed` |
| 15 | Expected-quiet override | **done** | live; `policy` tests |
| 16 | Late event from an old generation | **done** | `store`, `monitor`, smoke |
| 17 | One incident per condition, across restart | **done** | `store`, smoke |
| 18 | Uncertain delivery preserves the inbox | **done** | `store`, `monitor`, `notify` |
| 19 | Alerts reach the configured orchestrator | **done** | live — an alert delivered into a real Claude Code session, queued not interrupting |
| 20 | Two instances, alerts identify their own | **done** | live |
| 21 | Labels create no task state | **done** | `config`, `monitor` — carried, never read |

## What is not done

**Only one alert route exists.** `claude-inbox` delivers to a Claude Code
orchestrator; Codex and opencode orchestrators have no connector yet, and an
unknown kind is refused at startup rather than silently ignored. Adding one is
a new package and one import — see
[providers.md](./providers.md#notifying-the-orchestrator).

**opencode sessions must be launched with the plugin.**
`@brutalsystems/birddog-opencode` is published, so muster can install it by
specifier — but a session started without it exposes nothing an outside
process can find, and the adapter reports that rather than reporting silence.

**Permission requests are observable on opencode and on Claude Code.** An
instrumented opencode session and a Claude Code session started after
`birddog hooks install` both report `observed` or `not_observed` — the
difference between looking and being unable to look. Codex exposes no such
signal at all and still reports `unavailable`.

Both require per-user setup that birddog cannot do for anyone: the opencode
plugin has to be selected at launch, and hooks have to be installed into an
operator's own settings. Hooks do reach sessions already running, verified on
2.1.267.

Everything else in the proposed CLI is implemented, including `resume` and
idempotency keys on every mutation, plus `prune` and `--owner`, which the
handoff did not ask for — an instance outliving its orchestrator was the point,
but nothing reaped one whose orchestrator had gone for good.
Configurable event retention, which `handoff.md` asks for and which had no
implementation, now exists and is opt-in: see
[D7](./decisions.md#d7--event-retention-is-opt-in-and-a-terminal-outcome-outlives-it).

**Untested:** desktop applications, out of scope by
[D3](./decisions.md#d3--desktop-apps-are-out-of-scope-for-v1); Linux and
Windows, which the handoff says must not be claimed before their process
identity, IPC, filesystem and lifecycle behaviour have been tested.
