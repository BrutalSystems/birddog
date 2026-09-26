import { join } from 'node:path';

/**
 * Where the plugin publishes, and birddog reads.
 *
 * BIRDDOG_HOME is honoured so a test, or an operator running two setups, can
 * point both halves at the same alternative directory.
 */
export function birddogHome(env: Record<string, string | undefined>, home: string): string {
  return env.BIRDDOG_HOME && env.BIRDDOG_HOME.length > 0 ? env.BIRDDOG_HOME : join(home, '.birddog');
}

/** One record per live opencode session. */
export function sessionsDir(env: Record<string, string | undefined>, home: string): string {
  return join(birddogHome(env, home), 'opencode');
}

/**
 * Deliberately beside the records, not among them: birddog scans that
 * directory and sweeps it, and a log file dropped in would confuse both.
 */
export function pluginLogPath(env: Record<string, string | undefined>, home: string): string {
  return join(birddogHome(env, home), 'opencode-plugin.log');
}

export function sessionFile(dir: string, sessionID: string): string {
  return join(dir, `${sessionID}.json`);
}
