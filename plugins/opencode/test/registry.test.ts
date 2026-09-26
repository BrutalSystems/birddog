import { mkdtempSync, readFileSync, statSync, existsSync, readdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { Registry } from '../birddog-lib/registry.js';
import { unwritableDir } from './helpers.js';
import type { SessionSnapshot } from '../birddog-lib/types.js';

function snapshot(id = 'ses_1'): SessionSnapshot {
  return {
    session_id: id,
    slug: 'auth-refactor',
    title: 'Auth refactor',
    directory: '/work/api',
    opencode_version: '1.18.31',
    state: 'active',
    last_activity_at: '2026-09-21T12:00:00Z',
  };
}

function registry() {
  const dir = mkdtempSync(join(tmpdir(), 'bdreg-'));
  return { dir, reg: new Registry(dir, { pid: 4242, pluginVersion: '0.1.0' }) };
}

describe('publishing', () => {
  it('writes one record per session', async () => {
    const { dir, reg } = registry();
    await reg.publish([snapshot('ses_1'), snapshot('ses_2')]);

    expect(existsSync(join(dir, 'ses_1.json'))).toBe(true);
    expect(existsSync(join(dir, 'ses_2.json'))).toBe(true);
  });

  it('records who wrote it, so a stale record can be recognised', async () => {
    const { dir, reg } = registry();
    await reg.publish([snapshot()]);

    const rec = JSON.parse(readFileSync(join(dir, 'ses_1.json'), 'utf8'));
    expect(rec.pid).toBe(4242);
    expect(rec.plugin_version).toBe('0.1.0');
    expect(rec.updated_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
  });

  it('carries the state birddog reads', async () => {
    const { dir, reg } = registry();
    const s = snapshot();
    s.state = 'waiting_input';
    s.pending_permission = { id: 'p1', asked_at: '2026-09-21T12:00:00Z' };
    await reg.publish([s]);

    const rec = JSON.parse(readFileSync(join(dir, 'ses_1.json'), 'utf8'));
    expect(rec.state).toBe('waiting_input');
    expect(rec.pending_permission.id).toBe('p1');
  });

  // These records say which sessions an operator is running and where. They
  // are owner-only, like everything else birddog writes.
  it('keeps the directory and records owner-only', async () => {
    const { dir, reg } = registry();
    await reg.publish([snapshot()]);

    expect(statSync(dir).mode & 0o777).toBe(0o700);
    expect(statSync(join(dir, 'ses_1.json')).mode & 0o777).toBe(0o600);
  });

  // A reader can catch a half-written file at any moment, so writes land by
  // rename rather than in place.
  it('leaves no partial file behind', async () => {
    const { dir, reg } = registry();
    await reg.publish([snapshot()]);

    const stray = readdirSync(dir).filter((f) => !f.endsWith('.json'));
    expect(stray).toEqual([]);
  });

  it('removes the record of a session that has ended', async () => {
    const { dir, reg } = registry();
    await reg.publish([snapshot('ses_1'), snapshot('ses_2')]);
    await reg.publish([snapshot('ses_1')]);

    expect(existsSync(join(dir, 'ses_1.json'))).toBe(true);
    expect(existsSync(join(dir, 'ses_2.json'))).toBe(false);
  });

  // Only this process's records. Another opencode process has its own
  // sessions in the same directory, and sweeping them would blind birddog to
  // every session but ours.
  it('leaves another process records alone', async () => {
    const { dir, reg } = registry();
    writeFileSync(
      join(dir, 'ses_other.json'),
      JSON.stringify({ session_id: 'ses_other', pid: 9999 }),
      { mode: 0o600 },
    );

    await reg.publish([snapshot('ses_1')]);

    expect(existsSync(join(dir, 'ses_other.json'))).toBe(true);
  });

  it('removes this process records when the session list empties', async () => {
    const { dir, reg } = registry();
    await reg.publish([snapshot()]);
    await reg.publish([]);

    expect(existsSync(join(dir, 'ses_1.json'))).toBe(false);
  });

  // A record nobody can parse is worse than none: birddog would report a
  // session it cannot describe.
  it('ignores unreadable records when deciding what to sweep', async () => {
    const { dir, reg } = registry();
    writeFileSync(join(dir, 'broken.json'), '{not json', { mode: 0o600 });

    await expect(reg.publish([snapshot()])).resolves.not.toThrow();
    expect(existsSync(join(dir, 'ses_1.json'))).toBe(true);
  });
});

describe('robustness', () => {
  // The plugin runs inside opencode's worker thread. A failure here must
  // cost birddog its visibility, never the session its work.
  it('never throws when the directory cannot be written', async () => {
    const reg = new Registry(unwritableDir(), { pid: 1, pluginVersion: '0.1.0' });

    await expect(reg.publish([snapshot()])).resolves.not.toThrow();
  });

  it('reports the failure rather than hiding it', async () => {
    const reg = new Registry(unwritableDir(), { pid: 1, pluginVersion: '0.1.0' });
    await reg.publish([snapshot()]);

    expect(reg.lastError).toBeTruthy();
  });
});

// opencode instantiates the plugin twice in one process — every log shows two
// startups under one pid. Ownership by pid therefore makes the second
// instance's sweep delete the first instance's records, while the session is
// still running. birddog then reports observation lost for a healthy session.
describe('two plugin instances in one process', () => {
  it('does not sweep the other instance records', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'bdreg-'));
    const first = new Registry(dir, { pid: 4242, pluginVersion: '0.1.0' });
    const second = new Registry(dir, { pid: 4242, pluginVersion: '0.1.0' });

    await first.publish([snapshot('ses_first')]);
    // The second instance knows nothing of ses_first and publishes its own.
    await second.publish([snapshot('ses_second')]);

    expect(existsSync(join(dir, 'ses_first.json'))).toBe(true);
    expect(existsSync(join(dir, 'ses_second.json'))).toBe(true);
  });

  it('still sweeps its own ended sessions', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'bdreg-'));
    const reg = new Registry(dir, { pid: 4242, pluginVersion: '0.1.0' });

    await reg.publish([snapshot('ses_a'), snapshot('ses_b')]);
    await reg.publish([snapshot('ses_a')]);

    expect(existsSync(join(dir, 'ses_a.json'))).toBe(true);
    expect(existsSync(join(dir, 'ses_b.json'))).toBe(false);
  });

  it('records which instance wrote it', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'bdreg-'));
    const reg = new Registry(dir, { pid: 4242, pluginVersion: '0.1.0' });
    await reg.publish([snapshot()]);

    const rec = JSON.parse(readFileSync(join(dir, 'ses_1.json'), 'utf8'));
    expect(rec.instance_id).toBeTruthy();
  });
});
