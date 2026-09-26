import { randomBytes } from 'node:crypto';
import { chmod, mkdir, readFile, readdir, rename, unlink, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { sessionFile } from './paths.js';
import type { RegistryRecord, SessionSnapshot } from './types.js';

export interface RegistryContext {
  pid: number;
  pluginVersion: string;
  now?: () => Date;
}

/**
 * The plugin's half of the contract: one JSON record per live session, which
 * birddog reads and never writes.
 *
 * Nothing here can fail loudly. The plugin runs inside opencode's worker
 * thread, so a problem writing these records must cost birddog its visibility
 * and never cost the session its work — the failure is recorded on lastError
 * for the log instead of thrown at the host.
 */
export class Registry {
  /** lastError is how a failure is surfaced without throwing. */
  lastError?: string;

  private now: () => Date;

  /**
   * Identifies this plugin instance. opencode instantiates the plugin twice
   * in one process, so the pid is shared and cannot say who wrote a record —
   * and sweeping by pid made the second instance delete the first's records
   * for sessions that were still running.
   */
  private readonly instanceId = `inst-${randomBytes(6).toString('hex')}`;

  constructor(
    private dir: string,
    private ctx: RegistryContext,
  ) {
    this.now = ctx.now ?? (() => new Date());
  }

  /** publish writes a record for every live session and removes the records
   *  of sessions that have ended. */
  async publish(sessions: SessionSnapshot[]): Promise<void> {
    try {
      await mkdir(this.dir, { recursive: true, mode: 0o700 });
      // mkdir respects umask, so the mode is set explicitly.
      await chmod(this.dir, 0o700);

      const live = new Set<string>();
      for (const session of sessions) {
        live.add(session.session_id);
        await this.write(session);
      }
      await this.sweep(live);
      delete this.lastError;
    } catch (err) {
      this.lastError = err instanceof Error ? err.message : String(err);
    }
  }

  private async write(session: SessionSnapshot): Promise<void> {
    const record: RegistryRecord = {
      ...session,
      pid: this.ctx.pid,
      instance_id: this.instanceId,
      plugin_version: this.ctx.pluginVersion,
      // A heartbeat, not decoration: it is how birddog tells a session that
      // has gone quiet from a process that died, including when the pid has
      // since been reused by something else entirely.
      updated_at: `${this.now().toISOString().slice(0, 19)}Z`,
    };

    const final = sessionFile(this.dir, session.session_id);
    // Temp then rename: a reader can catch a file at any moment, and a
    // half-written record is worse than a slightly stale one. The suffix
    // carries the pid and a random token, because two writes inside one
    // process would otherwise collide on the same name.
    const temp = `${final}.${this.ctx.pid}.${randomBytes(4).toString('hex')}.tmp`;
    await writeFile(temp, JSON.stringify(record, null, 2), { mode: 0o600 });
    await rename(temp, final);
  }

  /**
   * sweep removes the records of sessions that have ended.
   *
   * Only this instance's own records. Another opencode process keeps its
   * sessions in the same directory, and so does the second plugin instance in
   * this one — sweeping either would blind birddog to sessions that are
   * running perfectly well.
   */
  private async sweep(live: Set<string>): Promise<void> {
    const entries = await readdir(this.dir);

    for (const name of entries) {
      if (!name.endsWith('.json')) continue;
      const sessionID = name.slice(0, -'.json'.length);
      if (live.has(sessionID)) continue;

      const path = join(this.dir, name);
      let record: { instance_id?: unknown };
      try {
        record = JSON.parse(await readFile(path, 'utf8'));
      } catch {
        // Unreadable, so unattributable. Leaving it costs nothing: birddog
        // cannot parse it either, and will not report a session from it.
        continue;
      }
      if (record.instance_id !== this.instanceId) continue;

      try {
        await unlink(path);
      } catch {
        // Already gone, or not ours to remove.
      }
    }
  }
}
