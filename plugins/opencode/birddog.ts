/**
 * birddog — opencode plugin.
 *
 * Publishes this opencode process's session state to `~/.birddog/opencode/`,
 * where birddog reads it. A default opencode TUI opens no port and writes no
 * pid file, so nothing outside the process can find it; this plugin is the
 * only way an opencode session is observable at all.
 *
 * It is read-only with respect to the session. It reports what opencode tells
 * it and never acts: no prompts, no approvals, no changes to a tool's
 * arguments, and no exception that could block one.
 *
 * WARNING: opencode's loader invokes EVERY exported function in this file as
 * a plugin, and does not descend into subdirectories. Export exactly one
 * thing, and never a `default`. All logic lives in ./birddog-lib/.
 */
import { appendFileSync, chmodSync, mkdirSync, statSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname } from 'node:path';
import { pluginLogPath, sessionsDir } from './birddog-lib/paths.js';
import { startPlugin } from './birddog-lib/plugin.js';

/** Rotated past this: small enough to stay cheap to read, big enough to hold
 *  a long session's diagnostics. */
const MAX_LOG_BYTES = 2 * 1024 * 1024;

/** version is kept in step with package.json by scripts/sync-version.mjs, and
 *  a test asserts they agree. */
const VERSION = '1.0.1';

export const Birddog = async (input?: { client?: { _client?: unknown } }) => {
  const logPath = pluginLogPath(process.env, homedir());

  // console.error would land in the TUI opencode is drawing its interface on,
  // and nowhere an operator can read afterwards. A file is the only way a
  // failure is visible once the session ends.
  //
  // This runs on opencode's worker thread, so the steady state is one syscall
  // per line: the mkdir, the chmod and the size check happen on the first
  // write only. The sink must not throw either — a logging failure must never
  // reach the host.
  let logBytes = -1;
  let tightened = false;
  const log = (line: string) => {
    try {
      const data = `${line}\n`;
      if (logBytes < 0) {
        mkdirSync(dirname(logPath), { recursive: true, mode: 0o700 });
        try {
          logBytes = statSync(logPath).size;
        } catch {
          logBytes = 0;
        }
      }
      if (logBytes >= MAX_LOG_BYTES) {
        // Truncate rather than rotate: these are diagnostics, not a record
        // anyone is entitled to keep. Writing rather than appending is what
        // truncates; resetting the counter alone would leave the file to grow
        // forever, one bound's worth at a time.
        writeFileSync(logPath, data, { mode: 0o600 });
        logBytes = Buffer.byteLength(data, 'utf8');
      } else {
        appendFileSync(logPath, data, { mode: 0o600 });
        logBytes += Buffer.byteLength(data, 'utf8');
      }
      if (!tightened) {
        // `mode` on either write only applies when the file is created, so
        // a pre-existing world-readable log is tightened here too.
        chmodSync(logPath, 0o600);
        tightened = true;
      }
    } catch {
      // Nowhere left to report it. Losing a log line is not worth disturbing
      // the session over.
    }
  };

  // opencode hands the plugin its own client. The transport underneath is
  // what lets birddog ask which sessions already exist, rather than only
  // learning about ones that begin after it loads.
  const transport = input?.client?._client;
  const usable =
    transport && typeof (transport as { get?: unknown }).get === 'function'
      ? (transport as { get(args: { url: string }): Promise<{ data?: unknown; response?: { status?: number } }> })
      : undefined;
  if (!usable) {
    log('[birddog] event=transport.missing detail=cannot read existing sessions');
  }

  return startPlugin({
    dir: sessionsDir(process.env, homedir()),
    pid: process.pid,
    pluginVersion: VERSION,
    log,
    ...(usable ? { transport: usable } : {}),
  });
};
