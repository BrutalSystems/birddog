import type { CurrentTool, PendingPermission, SessionSnapshot, SessionState } from './types.js';

/**
 * The plugin's view of the sessions in this opencode process.
 *
 * Pure: it holds no handles and performs no I/O, so every transition below is
 * testable without opencode running. It only ever reads what opencode reports.
 * Nothing here can act on a session.
 */

/** What opencode hands a tool hook. The shape is not documented, so every
 *  field is optional and anything missing means the event is not attributable
 *  rather than attributable to a guess. */
export interface ToolEvent {
  sessionID?: string;
  tool?: string;
  callID?: string;
}

interface Tracked {
  snapshot: SessionSnapshot;
  /** Outstanding tool calls by id. A session is working until the last one
   *  finishes: clearing on the first would report a busy session as free. */
  runningTools: Map<string, CurrentTool>;
}

export class SessionTracker {
  private sessions = new Map<string, Tracked>();

  /** unattributed counts events that named no session birddog knows. It is a
   *  diagnostic, not an error: it says coverage is incomplete rather than
   *  silently narrowing what is reported. */
  unattributed = 0;

  /** Sessions named by events that are not tracked yet. */
  private unknown = new Set<string>();

  constructor(private now: () => Date = () => new Date()) {}

  /** Every tracked session, oldest id first for stable output. */
  all(): SessionSnapshot[] {
    return [...this.sessions.values()].map((t) => t.snapshot);
  }

  get(sessionID: string): SessionSnapshot | undefined {
    return this.sessions.get(sessionID)?.snapshot;
  }

  /**
   * adoptOne records a session the tracker never saw begin.
   *
   * Driven by takeUnknown, so only sessions this process actually emits
   * events for are ever adopted. Fetching opencode's whole session list
   * instead would pull in its entire history — a hundred sessions that ended
   * days ago — and republish them under this process's pid with a fresh
   * heartbeat, which is precisely how birddog decides something is live.
   */
  adoptOne(session: unknown): void {
    const info = asRecord(session);
    if (!info || typeof info.id !== 'string') return;
    if (this.sessions.has(info.id)) return;
    this.upsert(info);
  }

  /**
   * takeUnknown returns the sessions named by events that this tracker does
   * not know, and forgets them. The caller is expected to go and resolve
   * them; asking again must not repeat work already in flight.
   */
  takeUnknown(): string[] {
    const ids = [...this.unknown];
    this.unknown.clear();
    return ids;
  }

  /** apply folds one opencode event into the view. It never throws: opencode
   *  is handing us its event stream, and a plugin that throws into it is a
   *  plugin interfering with the session it is supposed to be watching. */
  apply(event: unknown): void {
    const e = asRecord(event);
    if (!e) return;
    const props = asRecord(e.properties) ?? {};

    switch (e.type) {
      case 'session.created':
      case 'session.updated':
        this.upsert(props.info);
        return;

      case 'session.deleted': {
        // Read leniently: a delete needs nothing but an id, and refusing a
        // sparse payload leaves a session birddog keeps reporting as live.
        const id = asRecord(props.info)?.id;
        if (typeof id === 'string') this.sessions.delete(id);
        return;
      }

      case 'session.idle':
        this.setBaseState(props.sessionID, 'idle');
        return;

      case 'session.status': {
        // Only "idle" is idle. Anything opencode adds later is "not free",
        // which is the safe reading — claiming idle wrongly invites a nudge
        // nobody wanted.
        const type = asRecord(props.status)?.type;
        this.setBaseState(props.sessionID, type === 'idle' ? 'idle' : 'active');
        return;
      }

      // The shape below was captured from opencode 1.18.31 rather than
      // inferred. `permission` is the kind being asked for ("bash", "edit"),
      // a plain string — not an object carrying the id. The id is top level
      // on asked, and comes back as `requestID` on replied.
      case 'permission.asked':
        this.permissionAsked(props);
        return;

      case 'permission.replied':
        this.permissionReplied(props.sessionID, props.requestID);
        return;

      default:
        return;
    }
  }

  /** toolStarted records a tool beginning. Called from tool.execute.before,
   *  which must never throw — see the plugin entry point. */
  toolStarted(event: ToolEvent): void {
    const tracked = this.resolve(event.sessionID);
    if (!tracked) return;

    const call = event.callID ?? `${event.tool ?? 'tool'}-${tracked.runningTools.size}`;
    tracked.runningTools.set(call, {
      name: event.tool ?? 'unknown',
      started_at: this.stamp(),
    });
    this.touch(tracked);
  }

  /** toolFinished records a tool completing. A completion with no matching
   *  start is dropped rather than decremented, so a stray one cannot leave a
   *  session reporting work forever. */
  toolFinished(event: ToolEvent): void {
    const tracked = this.resolve(event.sessionID);
    if (!tracked) return;

    if (event.callID !== undefined) {
      tracked.runningTools.delete(event.callID);
    } else {
      // No call id: clear one matching entry by name, if there is one.
      for (const [id, tool] of tracked.runningTools) {
        if (tool.name === event.tool) {
          tracked.runningTools.delete(id);
          break;
        }
      }
    }
    this.touch(tracked);
  }

  private upsert(raw: unknown): void {
    const info = asRecord(raw);
    if (!info) return;
    const { id, slug, title, directory, version } = info;
    if (
      typeof id !== 'string' ||
      typeof slug !== 'string' ||
      typeof title !== 'string' ||
      typeof directory !== 'string' ||
      typeof version !== 'string'
    ) {
      // Not enough to describe a session. Reporting a partial one would put
      // a target in birddog's listing that nobody could identify.
      return;
    }

    const existing = this.sessions.get(id);
    if (existing) {
      Object.assign(existing.snapshot, { slug, title, directory, opencode_version: version });
      this.touch(existing);
      return;
    }

    this.sessions.set(id, {
      snapshot: {
        session_id: id,
        slug,
        title,
        directory,
        opencode_version: version,
        state: 'active',
        last_activity_at: this.stamp(),
      },
      runningTools: new Map(),
    });
  }

  private permissionAsked(props: Record<string, unknown>): void {
    const tracked = this.resolve(props.sessionID);
    if (!tracked) return;

    const pending: PendingPermission = {
      id: typeof props.id === 'string' ? props.id : 'unknown',
      asked_at: this.stamp(),
    };
    if (typeof props.permission === 'string') {
      pending.type = props.permission;
    }
    const detail = requestDetail(asRecord(props.metadata));
    if (detail !== undefined) {
      pending.detail = detail;
    }

    tracked.snapshot.pending_permission = pending;
    this.touch(tracked);
  }

  private permissionReplied(sessionID: unknown, permissionID: unknown): void {
    const tracked = this.resolve(sessionID);
    if (!tracked) return;

    const pending = tracked.snapshot.pending_permission;
    // A reply naming a different request must not clear the one still
    // waiting, or a session stays blocked while reporting that it is not.
    if (pending && typeof permissionID === 'string' && pending.id !== permissionID) return;

    delete tracked.snapshot.pending_permission;
    this.touch(tracked);
  }

  private setBaseState(sessionID: unknown, state: SessionState): void {
    const tracked = this.resolve(sessionID);
    if (!tracked) return;

    if (state === 'idle') {
      // An idle session is not running a tool. Without this, a completion
      // that never arrives leaves the session reporting running_tool
      // forever — which happens because opencode instantiates the plugin
      // twice and the tool hooks do not reliably reach both instances.
      //
      // A pending permission is not cleared here: it outranks idle, and is
      // the usual reason a session sits idle with work still to do.
      tracked.runningTools.clear();
    }

    tracked.snapshot.state = state;
    this.touch(tracked);
  }

  /** resolve finds the session an event belongs to, counting the ones that
   *  cannot be attributed instead of guessing at the most recent. */
  private resolve(sessionID: unknown): Tracked | undefined {
    if (typeof sessionID !== 'string') {
      this.unattributed++;
      return undefined;
    }
    const tracked = this.sessions.get(sessionID);
    if (!tracked) {
      this.unattributed++;
      // Worth resolving: an event naming it means this process is serving it.
      this.unknown.add(sessionID);
    }
    return tracked;
  }

  /** touch recomputes the reported state and the activity stamp.
   *
   *  Precedence: waiting on a human outranks everything — it is what an
   *  orchestrator most needs to hear, and a tool running underneath does not
   *  make the session unblocked. A running tool outranks the base state,
   *  because work in progress is not silence. */
  private touch(tracked: Tracked): void {
    const { snapshot, runningTools } = tracked;

    if (snapshot.pending_permission) {
      snapshot.state = 'waiting_input';
      delete snapshot.current_tool;
    } else if (runningTools.size > 0) {
      snapshot.state = 'running_tool';
      snapshot.current_tool = oldest(runningTools);
    } else {
      delete snapshot.current_tool;
      if (snapshot.state === 'waiting_input' || snapshot.state === 'running_tool') {
        // The thing that explained the state is over; the session is working
        // again until opencode says otherwise.
        snapshot.state = 'active';
      }
    }
    snapshot.last_activity_at = this.stamp();
  }

  private stamp(): string {
    return `${this.now().toISOString().slice(0, 19)}Z`;
  }
}

/** oldest returns the longest-running tool, which is the one worth naming. */
function oldest(tools: Map<string, CurrentTool>): CurrentTool {
  let chosen: CurrentTool | undefined;
  for (const tool of tools.values()) {
    if (!chosen || tool.started_at < chosen.started_at) chosen = tool;
  }
  // The map is non-empty at every call site.
  return chosen as CurrentTool;
}

/**
 * requestDetail summarises what a request is asking to do, so an alert can
 * say "waiting to run rm test.tst" rather than only "waiting on approval".
 *
 * Deliberately narrow: metadata also carries the full diff of an edit, which
 * can be thousands of lines. birddog records evidence, not payloads.
 */
const MAX_DETAIL = 200;

function requestDetail(metadata: Record<string, unknown> | undefined): string | undefined {
  if (!metadata) return undefined;
  const value = metadata.command ?? metadata.filepath;
  if (typeof value !== 'string' || value === '') return undefined;
  return value.length > MAX_DETAIL ? `${value.slice(0, MAX_DETAIL)}…` : value;
}

/**
 * sameDirectory compares two paths allowing for macOS reporting /tmp as
 * /private/tmp, which would otherwise make every launch under /tmp adopt
 * nothing at all.
 */
function sameDirectory(a: unknown, b: string): boolean {
  if (typeof a !== 'string') return false;
  return normalise(a) === normalise(b);
}

function normalise(p: string): string {
  const trimmed = p.replace(/\/+$/, '');
  return trimmed.startsWith('/private/') ? trimmed.slice('/private'.length) : trimmed;
}

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return typeof v === 'object' && v !== null ? (v as Record<string, unknown>) : undefined;
}
