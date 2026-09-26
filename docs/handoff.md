# birddog — development handoff

> **Status: Rev 1 — superseded in part.** Preserved verbatim as the original
> specification. The Codex, OpenCode and "Orchestrator notifications" sections
> rest on assumptions contradicted by local inspection of the installed
> runtimes; see [providers.md](./providers.md) for what each one actually
> exposes and [review.md](./review.md) for what it means. Everything not named
> there still stands.
>
> Owner: Mike Williams · Rev 1, 2026-09-21

## Purpose

Build **birddog**, a local, instance-based session monitor that helps an orchestration agent monitor coding agents and related work on one machine. Its initial platform is macOS. Structure the implementation so Linux and Windows support can follow without rewriting the monitoring policy or state model.

The user works with Claude Code, Codex, and OpenCode, in interactive terminals, desktop apps, and programmatically launched workers. Native communication is preferred where available. A tool called **tincan** bridges communication where a native route does not exist. Its interface and delivery semantics must be discovered; do not invent them.

The orchestrator must be able to start a birddog instance, tell it exactly what matters for that instance, observe its findings, update its watch list, and stop it. birddog must continue monitoring when the orchestrator's own agent turn ends.

This document specifies the proposed implementation. Interface names, defaults, and architecture below are design choices, not claims about an existing birddog product.

## Product boundaries

birddog reports what it observes and what meets configured attention rules. The orchestrator decides what those observations mean and what to do next. Task management, progress assessment, nudging, and worker lifecycle control belong to the orchestrator.

The central boundary: **birddog monitors sessions; it does not manage their work.**

Initial scope:

- Multiple independent monitoring instances on one host.
- Explicitly registered targets, including interactive terminal sessions, desktop sessions, and workers launched programmatically by other tools.
- Passive observation, structured status, durable events, and configurable attention policies.
- Provider adapters for Claude Code, Codex, and OpenCode, with honest capability reporting.
- Generic process and explicitly selected file/log observation as fallbacks.
- Optional delivery of attention alerts to the orchestrator through a configured native or tincan route.

Out of scope for the first release:

- Task ledgers, assignments, task states, acceptance criteria, dependency management, and judgments about completion or meaningful progress.
- Sending prompts or continuations to watched workers, including automatic nudges.
- Launching, killing, restarting, replacing, or otherwise controlling workers.
- Scheduling a development project or replacing native agent orchestration.
- Automatically approving permission requests, deciding business questions, merging, pushing, or deploying.
- Remote-machine monitoring, cloud services, multi-user hosting, and a full graphical dashboard.
- Terminal keystroke injection, screen scraping, OCR, or inferring progress from desktop animation.
- Automatically discovering and monitoring every agent session on the machine.

## Operating model

An **instance** is a durable monitoring scope started for an orchestration session or a particular project effort. It is not tied to the lifetime of the shell that started it.

An instance owns a watch list, policies, observations, events, and an optional delivery route to its orchestrator. A **target** is one specific agent session or process within that scope. The same application may host many targets; identifying an application process alone does not identify an agent session.

Start with one background process per instance. Use a foreground mode for debugging. Do not require a permanently installed system service, administrator privileges, or a LaunchAgent for the MVP. Persist state so a stopped instance can be explicitly resumed.

Multiple instances may observe the same target independently. They do not acquire ownership of the target or authority to control it. Deduplicate notifications within each instance; identify the instance in every alert so the orchestrator can distinguish overlapping watches.

Stopping birddog leaves all watched agents running. Make this explicit in CLI help. Use a per-instance lock to prevent duplicate daemon processes from running the same instance.

## Orchestrator workflow and proposed CLI

Provide a stable CLI with JSON output suitable for agents and human-readable output for terminal use. Commands acting on an existing instance accept an explicit instance ID; do not depend on a mutable implicit current instance.

Illustrative workflow:

```sh
birddog start --config ./birddog.json --json
birddog status --instance <instance-id> --json
birddog watch add --instance <instance-id> --file ./target.json --json
birddog watch update --instance <instance-id> --target worker-1 --file ./target-update.json --json
birddog events --instance <instance-id> --after <cursor> --wait 30 --json
birddog stop --instance <instance-id> --json
birddog resume --instance <instance-id> --json
```

Also provide `list`, `doctor`, `watch remove`, and an explicit command to acknowledge attention events. `doctor` reports versions, adapter capabilities, missing attachment information, and unsupported features without mutating agent sessions.

`start` returns the instance ID, state location, process identity, and initial adapter attachment results. It reports success only after the instance is reachable. Individual target failures should be visible without preventing healthy targets from being watched.

`events` supports bounded long polling and a durable cursor. Cursor replay must work across birddog restarts; a stale or invalid cursor must produce an explicit result rather than silently skip events. `status` returns current state plus the latest cursor, allowing snapshot-and-follow without a gap.

Mutations should support idempotency keys. Configuration updates must be validated and applied atomically. Unknown fields should be rejected for a given schema version. Errors need stable codes, readable explanations, and nonzero exit status.

Keep the CLI backend reusable so a later MCP tool or native integration can call the same service layer. An MCP server is not required for the MVP.

## Configuration: what matters to this instance

Support a versioned JSON configuration. An illustrative shape follows; the developer may refine syntax while preserving these semantics.

```json
{
  "schema_version": 1,
  "name": "checkout-refactor",
  "notification": {"kind": "none"},
  "targets": [
    {
      "id": "worker-1",
      "provider": "codex",
      "attachment": {
        "kind": "existing-session",
        "session_id": "explicit-session-id",
        "endpoint_ref": "local-codex-runtime"
      },
      "workspace": "/absolute/path/to/worktree",
      "labels": {"task_id": "T-42", "description": "Checkout worker"},
      "policy": {
        "alert_on": ["idle", "input_requested", "exit", "quiet", "observation_lost"],
        "quiet_after_seconds": 300,
        "idle_grace_seconds": 30,
        "expected_quiet_until": null
      },
      "observations": {
        "files": [],
        "logs": []
      }
    }
  ]
}
```

`endpoint_ref` resolves through locally configured connection information; it does not imply Codex desktop exposes a discoverable public endpoint. Secrets belong in restricted connection storage or environment references, not emitted configuration or event logs.

Allow targets identified by a provider session or a verified process identity. File and log paths must be explicit. Generic file watches may support associated build jobs without requiring an agent provider. birddog attaches to workers launched elsewhere; it does not launch them.

Labels, including an optional task ID, are opaque correlation metadata. birddog must not interpret them as a task record or use them to infer completion.

Support temporary monitoring overrides such as `expected_quiet_until` and an optional explanatory label. An override suppresses only quiet/idle alerts during that interval; observations continue and input-request, exit, and observation-loss alerts remain enabled unless explicitly configured otherwise. Overrides expire automatically and establish a fresh quiet/idle grace period on expiry rather than emitting an immediate overdue alert. Provide an explicit alert-snooze mechanism if needed; it must never change the observed runtime state.

Avoid a general-purpose rules language in the MVP.

## State and evidence model

Record runtime observations only. Suggested runtime states:

- `active`
- `idle`
- `waiting_input`
- `running_tool`
- `disconnected`
- `exited`
- `unknown`

Adapter health is a separate dimension: `healthy`, `degraded`, or `unavailable`. Preserve last-known runtime state with its timestamp when observation is lost, but make its staleness explicit. Do not expose old state as a current fact.

Record separately:

- `last_contact_at`: communication with the adapter/runtime was confirmed.
- `last_activity_at`: a session-scoped action or output event occurred.
- Current tool/operation and its start time, where available.
- Observed permission/input request, provider request ID, and resolution event, where available.
- Source, evidence scope, observation time, and freshness of each conclusion.

Track adapter contact separately from agent contact. A timer in a hook collector cannot prove the agent is alive. A provider connection heartbeat cannot prove a specific session is active.

File writes, token output, process CPU, and log growth are activity evidence. birddog does not track meaningful progress, milestones, task completion, or whether a worker should continue. A turn-completed event can establish that a turn ended; it cannot establish that the assigned work finished.

Permission/input requests are observed runtime signals, not managed blockers. Preserve their source and any documented resolution signal. No observed request does not prove there is no blocker, particularly for natural-language questions or adapters with limited visibility. Report request visibility as observed, not observed, or unavailable; avoid a derived "unblocked" flag. On loss of telemetry, mark outstanding request information stale rather than assuming it was resolved.

Each event includes instance ID, target ID, opaque labels if present, session identity, a sequence/cursor, source, timestamp, event type, and a small evidence payload. Record session/run generation and watch-configuration revision so delayed observations cannot overwrite newer state or become attributed to a replacement session.

Persist using SQLite, keeping instance state separate from repositories and worktrees. Use a platform-specific user state directory with restrictive permissions. Maintain a small machine-wide registry for instance discovery only. Serialize state changes and use appropriate database transactions.

## Adapter contract

Keep provider observation adapters separate from orchestrator notification transports. An observation adapter must advertise capabilities instead of pretending all providers support the same operations:

- Attach to an existing session.
- Observe lifecycle events.
- Read a current status snapshot.
- Identify approval/input waits.
- Observe tool start/completion.
- Reconcile after reconnect.

An unsupported observation capability must return a clear result. Worker message-delivery capabilities are outside this contract.

Observation methods must never resume a conversation, start a turn, or inject a prompt just to determine whether it is active. Snapshot reads must be verified to be read-only. Attaching to a different runtime from the one hosting a session does not establish live observation.

Implement reconnect with bounded backoff and reconciliation. Deduplicate repeated provider events where possible. If a provider offers no replay, record the observation gap and obtain a fresh snapshot; do not fabricate events for the gap.

### Claude Code

Investigate documented lifecycle hooks including `PreToolUse`, `PostToolUse`, `PermissionRequest`, `Stop`, `SubagentStop`, and, where applicable, `TeammateIdle`.

Use short, non-blocking hook handlers to forward structured observations to birddog. Telemetry failure must not block or modify Claude execution. Hook configuration installation must be explicit and preserve existing configuration. Determine whether changes affect already-running sessions before claiming attachment success.

A `Stop` event ends an execution turn; it does not prove the work is complete. Permission hooks do not cover every possible natural-language blocking decision; report this visibility limitation. Hook handlers must only report observations, never force continuation or approve requests.

### Codex

Investigate the App Server lifecycle/status protocol, including `thread/status/changed`, turn/item events, and approval flags. For workers hosted by a runtime birddog can connect to, normalize these signals.

Validate actual attachment support for the installed CLI and desktop versions. Do not assume launching a new App Server observes an independently running desktop or terminal session. If supported read-only live attachment is unavailable, expose reduced observation capabilities and use explicitly registered process/log evidence. Do not reverse-engineer private desktop databases as the primary MVP contract.

Native task observation tools available inside an orchestrator's Codex session are not automatically callable by an external birddog process. An explicit bridge may forward their observations, with freshness and source recorded.

### OpenCode

Investigate the server's session status API, event stream, and plugin lifecycle/permission events. Connect to the runtime actually hosting the target session. Validate the installed version's API rather than assuming a particular port or schema.

Register endpoint and session identity explicitly. A healthy server is not evidence that every hosted session is active. Do not use prompt or session-control endpoints for observation.

### Generic macOS observation

Support explicit process registration with PID plus process start identity to protect against PID reuse. Track descendants where practical, since a worker may be waiting on a child build or test process. Application-wide process activity must not be attributed to an individual session without a reliable mapping.

Support selected log/file changes, bounded reads, truncation/rotation handling, and polling fallback. Avoid recursive whole-disk or whole-home watching. Report inaccessible evidence as unavailable.

macOS sleep/wake must not produce a storm of false stale alerts: on wake, reconcile targets and allow a grace period. Use monotonic elapsed time for live timers and wall-clock timestamps for persisted records. Treat resume after process restart as a new observation baseline.

## Attention policy

birddog always observes workers passively. Configured rules produce attention events; they never trigger worker commands.

| Evidence | Action |
|---|---|
| Active, recent session activity | Continue observing |
| Observed permission or input request | Surface an input-request event with available evidence |
| Confirmed idle for the configured grace period | Surface an idle event if that rule is enabled |
| No relevant activity past configured interval | Surface a quiet event with evidence coverage and freshness |
| Lost connection or observation failure | Surface observation loss; retry attachment |
| Verified process/session exit | Surface exit with last-known state |
| Activity resumes or an observed request resolves | Record the transition and resolve the corresponding attention condition |

An idle alert means "idle was observed," not "the worker stopped prematurely." A quiet alert means "no relevant activity was observed within the interval," not "stalled." Distinguish a silent but healthy event connection from a broken connection. If visibility is insufficient to establish silence, report observation loss or unknown state instead.

Initial defaults may be five minutes for quiet attention and thirty seconds for idle grace. These are configurable heuristics. Respect temporary monitoring overrides and report provider retry/backoff information as context when available. Do not infer a blocker or expected duration from arbitrary worker prose.

Open one incident per target/run generation and attention condition. Deduplicate repeated observations, persist incident state across restarts, and emit a resolution when supported by fresh evidence. Notify on opening or material change; repeated reminder notifications are off by default. When input-wait or idle state already explains silence, include quiet duration in that incident rather than opening redundant alerts.

Acknowledging an alert only records that the orchestrator has seen it. It does not resolve a provider request, approve an operation, or alter the worker. Threshold and override updates affect future classification and preserve the event history.

An example alert:

```text
Worker 2 has been idle for 45 seconds.
Last observed event: turn completed.
Permission/input request: none observed; coverage is limited.
Source: provider lifecycle events. Last observation: 2 seconds ago.
```

The orchestrator decides whether to inspect, nudge, ask the user, change the watch policy, or remove the target.

## Orchestrator notifications: native routes and tincan

Configure an explicit notification route for the instance: native integration, tincan, or none. The recipient is the orchestrator, not a watched worker. Prefer a working native route where one exists; use tincan where it supplies the missing path. birddog does not route assignments or worker continuations.

Discover tincan's installed interface or obtain its documentation before implementing its notification transport. Establish recipient identity, delivery acknowledgement, errors, idempotency support, and effects on an active orchestrator turn. An asynchronous API call does not establish safe queue behavior. If non-disruptive delivery cannot be established, retain alerts in birddog's inbox for polling rather than injecting messages into a running conversation.

Keep every attention event available through `events` and `status` regardless of notification transport availability. If the orchestrator is stopped or unreachable, monitoring continues and alerts remain durable. Notification failure must not affect observation.

Provide a notification interface with a fake implementation for tests. Do not make the monitoring core depend on tincan. Attach stable event/incident IDs to notifications. Use bounded retries for definite failures; after uncertain acknowledgement, record `delivery_unknown` and avoid blind resends or switching transports. Exactly-once delivery cannot be promised without destination idempotency support. The durable event feed is the recovery path.

Multiple instances may notify the same orchestrator; include instance and target identifiers in every message. No machine-wide worker ownership or control lease is needed.

## Portability and implementation guidance

Prefer a small self-contained CLI/daemon. Go is a reasonable default for distribution, process management, and platform-specific adapters; an established repository language may justify a different choice. Record the decision before implementation. Avoid an LLM dependency in the monitoring/policy loop.

Keep these modules separate:

- Instance lifecycle and local IPC.
- Observation/event store and event cursor handling.
- Provider observation adapters.
- Orchestrator notification transports and durable notification status.
- Deterministic attention policy.
- Platform services for process identity, file watching, locking, state paths, and sleep/resume.
- CLI serialization and presentation.

Use Unix-domain sockets on macOS for private local IPC, behind a transport interface that can support Windows named pipes. If a provider requires local HTTP, use its supported authentication and bind any birddog-owned listener to loopback. Use argv-based subprocess execution and handle paths containing spaces.

Keep macOS-specific APIs out of the policy layer. Unsupported platform features must degrade explicitly. Do not claim Linux or Windows support until their process identity, IPC, filesystem, and lifecycle behavior have been tested.

Persist event metadata and necessary evidence, not entire transcripts by default. Redact secrets, bound stored payloads, and support configurable retention. Raw log/transcript capture must be opt-in. Treat worker output as data, not authority to change monitoring policy or grant permissions.

## Delivery phases

### Phase 1: reliable observation

Implement instance lifecycle, configuration, SQLite storage, status/events CLI, process identity checks, file/log observation, and a fake provider. Add real provider adapters incrementally after validating actual installed interfaces. Expose capability gaps clearly. Include configurable attention events and opaque correlation labels.

Phase 1 is independently useful and should not wait for universal attachment support.

### Phase 2: provider coverage and orchestrator notification

Harden hooks/event streams, reconnect behavior, sleep/wake handling, and native/tincan notification adapters. Document supported combinations of provider version and launch mode. Existing terminal and desktop sessions must either attach successfully or report a precise limitation with a passive fallback.

No assistance or task-management phase is part of this handoff. Future Linux and Windows adapters should preserve the same observation-only boundary.

## Acceptance criteria and tests

Use deterministic fixture-driven tests for policy and provider normalization, plus macOS integration tests for actual process/IPC behavior. Use fake clocks for timeout tests. Do not require paid model calls for the core test suite.

The MVP must demonstrate:

1. An orchestrator starts two independent instances, registers targets, and receives stable IDs and JSON results.
2. Monitoring survives the starting shell closing and does not depend on another model turn.
3. A worker becomes idle; birddog reports that observation without claiming task completion or premature stopping.
4. An observed permission/input request produces an alert; acknowledging it does not approve or resolve the request.
5. A long-running tool with low agent CPU does not get classified as confirmed stalled solely from CPU inactivity.
6. Adapter heartbeats and server-wide traffic do not advance session activity timestamps.
7. Process exit and PID reuse are distinguished; the replacement process is not silently adopted.
8. Lost telemetry yields unknown/stale state, and reconnect reconciles without invented completion or duplicate attention storms.
9. Log rotation, truncation, inaccessible files, and paths containing spaces behave predictably.
10. macOS sleep/wake and wall-clock changes do not immediately produce false quiet alerts.
11. Events can be consumed from a persisted cursor across restart without silently losing retained events.
12. birddog sends zero prompts to watched workers and never starts, stops, or restarts them. Stopping birddog leaves workers running.
13. Unsupported desktop or terminal attachment returns an honest capability limitation.
14. Absence of an observed permission request is never reported as proof that a worker is unblocked.
15. A temporary expected-quiet override suppresses quiet/idle alerts while recording observations; exit/input alerts still work, and expiry establishes a fresh grace period.
16. A late event from an old session generation cannot update a replacement session's state.
17. Repeated observations of the same attention condition produce one incident, including across restart, until it resolves or materially changes.
18. Unavailable or uncertain notification delivery preserves the event inbox without blindly resending through another transport.
19. Native or tincan alerts target the configured orchestrator only, with tested non-disruptive delivery semantics or a polling fallback.
20. Two instances can observe the same target without acquiring worker control; their alerts identify the originating instance.
21. Opaque task labels do not create task records, dependency state, acceptance criteria, or completion logic.

Supply a macOS smoke-test script using fake agents that simulate active output, quiet work, idle, approval waits, tool execution, disconnects, and exits. Provide sample configs for each supported provider/mode, an architecture note, CLI reference, and a troubleshooting guide.

## Integration research the developer must finish

Before promising coverage, inventory the user's installed versions and available endpoints. Build a support matrix with rows for Claude Code, Codex, and OpenCode, and columns for terminal, desktop, and programmatic workers. Record observation, attachment, and input-request visibility separately. Document orchestrator notification transport capabilities independently.

Resolve these details through local inspection and official documentation where possible:

- Which existing sessions offer supported read-only live attachment?
- How are session IDs mapped to endpoints/processes without ambiguity?
- Can hooks be installed for existing sessions, and what requires a restart?
- Which notification routes can reach the orchestrator without disrupting its active turn?
- What is tincan's actual interface and acknowledgement behavior?

Ask the user only for information that cannot be determined safely. Unsupported capabilities are not a reason to delay the generic monitor or to simulate support.

## Documentation starting points

These official references were checked during the preceding design discussion. Revalidate them against installed versions before implementing; the APIs may evolve.

- Claude Code hooks: https://code.claude.com/docs/en/hooks
- Codex App Server: https://learn.chatgpt.com/docs/app-server
- OpenCode server: https://opencode.ai/docs/server
- OpenCode plugins and events: https://opencode.ai/docs/plugins

## Definition of done

The user can give an orchestrator an instruction such as "Start birddog for these three sessions; tell me when one requests input, exits, becomes idle, or goes quiet for five minutes," and the orchestrator can do so through documented commands. It can also adjust a watch: "Expect this worker to be quiet for twenty minutes while its tests run."

The resulting instance monitors independently, reports evidence with explicit visibility limits, survives recoverable observation failures, and stops without disturbing the workers. The orchestrator retains all decisions about tasks, completion, approvals, nudging, and recovery.

**birddog tells the orchestrator what it observed and what deserves attention; the orchestrator decides what to do about it.**
