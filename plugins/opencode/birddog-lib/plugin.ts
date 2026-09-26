import { Registry } from './registry.js';
import { SessionTracker, type ToolEvent } from './state.js';

/** Transport is the bit of opencode's client birddog needs: one read. */
export interface Transport {
  get(args: { url: string }): Promise<{ data?: unknown; response?: { status?: number } }>;
}

export interface PluginOptions {
  dir: string;
  pid: number;
  pluginVersion: string;
  log: (line: string) => void;

  /**
   * transport reads opencode's own session list at startup, so a session that
   * already existed when the plugin loaded is still observed.
   */
  transport?: Transport;

  /** directory this process serves; adoption is limited to it. */
  directory?: string;
  /** How often to republish so birddog can tell a quiet session from a dead
   *  process. Zero disables the timer, which is what tests want. */
  heartbeatMs?: number;
}

export interface PluginHooks {
  event: (arg: { event: unknown }) => Promise<void>;
  'tool.execute.before': (input: unknown, output: unknown) => Promise<void>;
  'tool.execute.after': (input: unknown, output: unknown) => Promise<void>;
  stop: () => void;
}

/** defaultHeartbeatMs republishes often enough that a dead process is noticed
 *  promptly, rarely enough to cost nothing on a session doing real work. */
const defaultHeartbeatMs = 15_000;

/**
 * startPlugin wires the tracker to the registry.
 *
 * Every hook returned here is guarded. opencode's `tool.execute.before` can
 * block a tool by throwing — that is how its own env-protection example works
 * — so a throw from birddog would turn a monitoring failure into the agent's
 * failure. birddog observes; it does not get a vote on whether work proceeds.
 */
export function startPlugin(opts: PluginOptions): PluginHooks {
  const tracker = new SessionTracker();
  const registry = new Registry(opts.dir, { pid: opts.pid, pluginVersion: opts.pluginVersion });

  opts.log(`[birddog] event=started version=${opts.pluginVersion} pid=${opts.pid} dir=${opts.dir}`);

  let lastReported: string | undefined;
  const publish = async (): Promise<void> => {
    await registry.publish(tracker.all());
    if (registry.lastError && registry.lastError !== lastReported) {
      // Logged once per distinct failure: a broken disk should not fill the
      // log with the same line on every tool call.
      opts.log(`[birddog] event=publish.failed detail=${registry.lastError}`);
      lastReported = registry.lastError;
    } else if (!registry.lastError) {
      lastReported = undefined;
    }
  };

  // guard is the boundary. Nothing inside the plugin may reach opencode as an
  // exception, including a bug in birddog's own code.
  const guard = async (what: string, fn: () => Promise<void>): Promise<void> => {
    try {
      await fn();
    } catch (err) {
      try {
        opts.log(`[birddog] event=${what}.failed detail=${err instanceof Error ? err.message : String(err)}`);
      } catch {
        // Even logging must not throw into the host.
      }
    }
  };

  // Resolve sessions that events name but the tracker does not know. This is
  // how a session created before the plugin loaded — every muster launch —
  // becomes visible at all. Driven by events rather than by listing, so
  // opencode's session history is never pulled in.
  const resolveUnknown = async (): Promise<void> => {
    if (!opts.transport) return;
    for (const id of tracker.takeUnknown()) {
      const res = await opts.transport.get({ url: `/session/${id}` });
      if ((res.response?.status ?? 0) !== 200 || typeof res.data !== 'object' || res.data == null) {
        opts.log(`[birddog] event=resolve.failed session=${id} status=${res.response?.status ?? 0}`);
        continue;
      }
      tracker.adoptOne(res.data);
      opts.log(`[birddog] event=adopted session=${id}`);
    }
  };

  const heartbeat = opts.heartbeatMs ?? defaultHeartbeatMs;
  let timer: ReturnType<typeof setInterval> | undefined;
  if (heartbeat > 0) {
    timer = setInterval(() => void guard('heartbeat', publish), heartbeat);
    // Never keep opencode alive on birddog's account.
    timer.unref?.();
  }

  return {
    event: (arg) =>
      guard('event', async () => {
        tracker.apply(arg?.event);
        await resolveUnknown();
        await publish();
      }),

    // input carries the tool and session; output carries the arguments the
    // tool is about to run with. birddog reads neither's arguments and
    // changes nothing in them.
    'tool.execute.before': (input) =>
      guard('tool.before', async () => {
        tracker.toolStarted(asToolEvent(input));
        await resolveUnknown();
        await publish();
      }),

    'tool.execute.after': (input) =>
      guard('tool.after', async () => {
        tracker.toolFinished(asToolEvent(input));
        await publish();
      }),

    stop: () => {
      if (timer) clearInterval(timer);
    },
  };
}

/** asToolEvent reads only the fields needed to attribute the call. The hook's
 *  payload shape is undocumented, so anything absent makes the event
 *  unattributable rather than attributed to a guess. */
function asToolEvent(input: unknown): ToolEvent {
  if (typeof input !== 'object' || input === null) return {};
  const o = input as Record<string, unknown>;
  const event: ToolEvent = {};
  if (typeof o.sessionID === 'string') event.sessionID = o.sessionID;
  if (typeof o.tool === 'string') event.tool = o.tool;
  if (typeof o.callID === 'string') event.callID = o.callID;
  return event;
}
