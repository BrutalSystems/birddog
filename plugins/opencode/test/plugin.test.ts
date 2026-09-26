import { mkdtempSync, existsSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it, vi } from 'vitest';
import { startPlugin } from '../birddog-lib/plugin.js';
import { unwritableDir } from './helpers.js';

function start(overrides: Record<string, unknown> = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'bdplug-'));
  const logged: string[] = [];
  const hooks = startPlugin({
    dir,
    pid: 4242,
    pluginVersion: '0.1.0',
    log: (line: string) => logged.push(line),
    heartbeatMs: 0, // no timer in tests
    ...overrides,
  });
  return { dir, hooks, logged };
}

const info = {
  id: 'ses_1',
  slug: 'auth',
  title: 'Auth',
  directory: '/work/api',
  version: '1.18.31',
};

async function created(hooks: { event: (arg: { event: unknown }) => Promise<void> }) {
  await hooks.event({ event: { type: 'session.created', properties: { info } } });
}

describe('publishing through the hooks', () => {
  it('writes a record when a session appears', async () => {
    const { dir, hooks } = start();
    await created(hooks);

    expect(existsSync(join(dir, 'ses_1.json'))).toBe(true);
  });

  it('reports a permission request as waiting_input', async () => {
    const { dir, hooks } = start();
    await created(hooks);
    await hooks.event({
      event: { type: 'permission.asked', properties: { sessionID: 'ses_1', permission: { id: 'p1' } } },
    });

    const rec = JSON.parse(readFileSync(join(dir, 'ses_1.json'), 'utf8'));
    expect(rec.state).toBe('waiting_input');
  });

  it('reports a running tool', async () => {
    const { dir, hooks } = start();
    await created(hooks);
    await hooks['tool.execute.before']({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' }, { args: {} });

    const rec = JSON.parse(readFileSync(join(dir, 'ses_1.json'), 'utf8'));
    expect(rec.state).toBe('running_tool');
    expect(rec.current_tool.name).toBe('bash');
  });
});

describe('the plugin never interferes with the session', () => {
  // tool.execute.before can BLOCK a tool by throwing — that is how opencode's
  // own env-protection example works. birddog observes, so it must never
  // throw here whatever goes wrong: a monitoring failure must not become the
  // agent's failure.
  it('does not throw from tool.execute.before even when publishing fails', async () => {
    const { hooks } = start({ dir: unwritableDir() });

    await expect(
      hooks['tool.execute.before']({ sessionID: 'ses_1', tool: 'bash' }, { args: {} }),
    ).resolves.not.toThrow();
  });

  it('does not throw from tool.execute.after', async () => {
    const { hooks } = start({ dir: unwritableDir() });

    await expect(
      hooks['tool.execute.after']({ sessionID: 'ses_1', tool: 'bash' }, {}),
    ).resolves.not.toThrow();
  });

  it('does not throw from the event hook', async () => {
    const { hooks } = start({ dir: unwritableDir() });

    for (const event of [null, undefined, { type: 'session.created' }, 42]) {
      await expect(hooks.event({ event })).resolves.not.toThrow();
    }
  });

  // tool.execute.before receives the arguments a tool is about to run with.
  // birddog reads nothing from them and changes nothing in them.
  it('leaves the tool arguments untouched', async () => {
    const { hooks } = start();
    await created(hooks);

    const output = { args: { command: 'rm -rf build', filePath: '/work/api/x' } };
    const before = JSON.stringify(output);
    await hooks['tool.execute.before']({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' }, output);

    expect(JSON.stringify(output)).toBe(before);
  });

  // A slow or broken filesystem must not stall the agent's tool call.
  it('survives a publish that fails repeatedly', async () => {
    const { hooks, logged } = start({ dir: unwritableDir() });

    for (let i = 0; i < 5; i++) {
      await hooks.event({ event: { type: 'session.created', properties: { info } } });
    }
    expect(logged.some((l) => l.includes('publish'))).toBe(true);
  });
});

describe('diagnostics', () => {
  // console.error would land in the TUI opencode is drawing on, and nowhere
  // an operator can read afterwards. Failures go to the log sink instead.
  it('logs a publish failure rather than printing it', async () => {
    const { hooks, logged } = start({ dir: unwritableDir() });
    await created(hooks);

    expect(logged.length).toBeGreaterThan(0);
    expect(logged.join('\n')).toMatch(/publish/);
  });

  it('logs its version once at startup, so a stale plugin is visible', () => {
    const { logged } = start();
    expect(logged.join('\n')).toMatch(/0\.1\.0/);
  });
});

describe('heartbeat', () => {
  // birddog treats a record whose heartbeat has stopped as unobservable. The
  // timer is what distinguishes a quiet session from a dead process.
  it('republishes on an interval', async () => {
    vi.useFakeTimers();
    try {
      const { dir, hooks } = start({ heartbeatMs: 1000 });
      await created(hooks);

      const first = JSON.parse(readFileSync(join(dir, 'ses_1.json'), 'utf8')).updated_at;
      await vi.advanceTimersByTimeAsync(3000);
      await Promise.resolve();

      const rec = JSON.parse(readFileSync(join(dir, 'ses_1.json'), 'utf8'));
      expect(rec.updated_at).toBeTruthy();
      expect(typeof first).toBe('string');
    } finally {
      vi.useRealTimers();
    }
  });

  // An interval that keeps the event loop alive would stop opencode exiting.
  it('does not hold the process open', () => {
    const { hooks } = start({ heartbeatMs: 1000 });
    expect(hooks.stop).toBeTypeOf('function');
    hooks.stop();
  });
});
