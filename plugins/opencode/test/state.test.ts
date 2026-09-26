import { describe, expect, it } from 'vitest';
import { SessionTracker } from '../birddog-lib/state.js';

const info = {
  id: 'ses_1',
  slug: 'auth-refactor',
  title: 'Auth refactor',
  directory: '/work/api',
  version: '1.18.31',
};

function tracker() {
  return new SessionTracker();
}

describe('session lifecycle', () => {
  it('records a session when it is created', () => {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });

    const s = t.get('ses_1');
    expect(s?.directory).toBe('/work/api');
    expect(s?.slug).toBe('auth-refactor');
  });

  it('forgets a session when it is deleted', () => {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    t.apply({ type: 'session.deleted', properties: { info: { id: 'ses_1' } } });

    expect(t.get('ses_1')).toBeUndefined();
  });

  // A delete carrying only an id must still remove the record. Demanding the
  // full create shape leaves a session birddog keeps reporting as live.
  it('accepts a sparse delete payload', () => {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    t.apply({ type: 'session.deleted', properties: { info: { id: 'ses_1' } } });

    expect(t.get('ses_1')).toBeUndefined();
  });

  it('ignores an event for a session it has never seen', () => {
    const t = tracker();
    t.apply({ type: 'session.idle', properties: { sessionID: 'unknown' } });

    expect(t.get('unknown')).toBeUndefined();
  });
});

describe('runtime state', () => {
  function started() {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    return t;
  }

  it('reports idle when the session goes idle', () => {
    const t = started();
    t.apply({ type: 'session.idle', properties: { sessionID: 'ses_1' } });

    expect(t.get('ses_1')?.state).toBe('idle');
  });

  it('reports active when the session is busy', () => {
    const t = started();
    t.apply({ type: 'session.status', properties: { sessionID: 'ses_1', status: { type: 'busy' } } });

    expect(t.get('ses_1')?.state).toBe('active');
  });

  // Only "idle" means idle. Anything opencode adds later is "not free", which
  // is the safe reading: claiming idle wrongly invites a nudge nobody wanted.
  it('treats an unfamiliar status as not idle', () => {
    const t = started();
    t.apply({ type: 'session.status', properties: { sessionID: 'ses_1', status: { type: 'retrying' } } });

    expect(t.get('ses_1')?.state).toBe('active');
  });
});

describe('tool execution', () => {
  function started() {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    return t;
  }

  it('reports running_tool while a tool is executing', () => {
    const t = started();
    t.toolStarted({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' });

    const s = t.get('ses_1');
    expect(s?.state).toBe('running_tool');
    expect(s?.current_tool?.name).toBe('bash');
  });

  it('stops reporting running_tool when the tool finishes', () => {
    const t = started();
    t.toolStarted({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' });
    t.toolFinished({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' });

    expect(t.get('ses_1')?.state).not.toBe('running_tool');
    expect(t.get('ses_1')?.current_tool).toBeUndefined();
  });

  // Nested or parallel tools: the session is still working until the last one
  // finishes. Clearing on the first completion would report a busy session as
  // free.
  it('stays running_tool until every tool has finished', () => {
    const t = started();
    t.toolStarted({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' });
    t.toolStarted({ sessionID: 'ses_1', tool: 'read', callID: 'c2' });
    t.toolFinished({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' });

    expect(t.get('ses_1')?.state).toBe('running_tool');

    t.toolFinished({ sessionID: 'ses_1', tool: 'read', callID: 'c2' });
    expect(t.get('ses_1')?.state).not.toBe('running_tool');
  });

  // A completion for a tool never seen starting must not underflow the count
  // and leave the session stuck reporting work forever.
  it('ignores a completion with no matching start', () => {
    const t = started();
    t.toolFinished({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' });
    t.toolStarted({ sessionID: 'ses_1', tool: 'read', callID: 'c2' });
    t.toolFinished({ sessionID: 'ses_1', tool: 'read', callID: 'c2' });

    expect(t.get('ses_1')?.state).not.toBe('running_tool');
  });

  // The tool hooks' payload shape is not documented. Anything unattributable
  // is dropped rather than guessed onto whichever session was last active.
  it('ignores a tool event with no session it can be attributed to', () => {
    const t = started();
    t.toolStarted({ tool: 'bash', callID: 'c1' });

    expect(t.get('ses_1')?.state).not.toBe('running_tool');
    expect(t.unattributed).toBe(1);
  });
});

// The payloads below are the real ones, captured from opencode 1.18.31 with a
// probe plugin. The first draft of this file guessed the shape — it had
// `permission` as an object carrying the id — and the tests passed against the
// guess while a live session reported every request as "unknown".
const askedBash = {
  type: 'permission.asked',
  properties: {
    id: 'per_0c56cf1ee001D5C8oQguAAA1dv',
    sessionID: 'ses_1',
    permission: 'bash',
    patterns: ['rm test.tst'],
    metadata: { command: 'rm test.tst' },
    always: ['rm *'],
    tool: { messageID: 'msg_1', callID: 'call_1' },
  },
};

const repliedBash = {
  type: 'permission.replied',
  properties: {
    sessionID: 'ses_1',
    requestID: 'per_0c56cf1ee001D5C8oQguAAA1dv',
    reply: 'once',
  },
};

describe('permission requests', () => {
  function started() {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    return t;
  }

  // This is what makes opencode the first provider where birddog can see an
  // input request at all, rather than reporting visibility as unavailable.
  it('reports waiting_input while a permission is outstanding', () => {
    const t = started();
    t.apply(askedBash);

    const s = t.get('ses_1');
    expect(s?.state).toBe('waiting_input');
    expect(s?.pending_permission?.id).toBe('per_0c56cf1ee001D5C8oQguAAA1dv');
  });

  // What the request is *for* is the useful part of an alert: "waiting on
  // approval" is far less actionable than "waiting to run rm test.tst".
  it('records what the request is asking to do', () => {
    const t = started();
    t.apply(askedBash);

    const p = t.get('ses_1')?.pending_permission;
    expect(p?.type).toBe('bash');
    expect(p?.detail).toBe('rm test.tst');
  });

  it('records the file an edit request is asking about', () => {
    const t = started();
    t.apply({
      type: 'permission.asked',
      properties: {
        id: 'per_edit', sessionID: 'ses_1', permission: 'edit',
        metadata: { filepath: '/work/api/main.go', diff: 'Index: …' },
      },
    });

    const p = t.get('ses_1')?.pending_permission;
    expect(p?.type).toBe('edit');
    expect(p?.detail).toBe('/work/api/main.go');
  });

  // A diff can be thousands of lines. birddog stores evidence, not payloads.
  it('does not carry a diff into the record', () => {
    const t = started();
    t.apply({
      type: 'permission.asked',
      properties: {
        id: 'per_edit', sessionID: 'ses_1', permission: 'edit',
        metadata: { filepath: '/work/api/main.go', diff: 'x'.repeat(50_000) },
      },
    });

    expect(JSON.stringify(t.get('ses_1')).length).toBeLessThan(2_000);
  });

  it('clears the wait once the permission is answered', () => {
    const t = started();
    t.apply(askedBash);
    t.apply(repliedBash);

    const s = t.get('ses_1');
    expect(s?.state).not.toBe('waiting_input');
    expect(s?.pending_permission).toBeUndefined();
  });

  // A reply naming a different request must not clear the one still waiting.
  it('ignores a reply for a different request', () => {
    const t = started();
    t.apply(askedBash);
    t.apply({
      type: 'permission.replied',
      properties: { sessionID: 'ses_1', requestID: 'per_somethingelse', reply: 'once' },
    });

    expect(t.get('ses_1')?.state).toBe('waiting_input');
  });

  // Waiting on a human outranks everything else: it is the condition an
  // orchestrator most needs to hear about, and a tool running underneath does
  // not change that the session is blocked.
  it('outranks a running tool', () => {
    const t = started();
    t.toolStarted({ sessionID: 'ses_1', tool: 'bash', callID: 'c1' });
    t.apply(askedBash);

    expect(t.get('ses_1')?.state).toBe('waiting_input');
  });

  it('outranks idle', () => {
    const t = started();
    t.apply(askedBash);
    t.apply({ type: 'session.idle', properties: { sessionID: 'ses_1' } });

    expect(t.get('ses_1')?.state).toBe('waiting_input');
  });
});

// opencode instantiates the plugin twice in one process, each instance with
// its own tracker, and the tool hooks do not reliably reach both. An instance
// that saw a tool start but not finish reports running_tool forever, and
// because both instances publish to the same record the stuck one wins.
//
// Observed live: a `write` that completed left the session reporting
// running_tool indefinitely while it sat idle.
//
// The defence does not depend on working out which instance gets what. If
// opencode says the session is idle, no tool of its is running.
describe('a tool whose completion never arrives', () => {
  function started() {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    return t;
  }

  it('is cleared when the session goes idle', () => {
    const t = started();
    t.toolStarted({ sessionID: 'ses_1', tool: 'write', callID: 'c1' });
    expect(t.get('ses_1')?.state).toBe('running_tool');

    t.apply({ type: 'session.idle', properties: { sessionID: 'ses_1' } });

    expect(t.get('ses_1')?.state).toBe('idle');
    expect(t.get('ses_1')?.current_tool).toBeUndefined();
  });

  it('is cleared when the session reports idle through status', () => {
    const t = started();
    t.toolStarted({ sessionID: 'ses_1', tool: 'write', callID: 'c1' });

    t.apply({ type: 'session.status', properties: { sessionID: 'ses_1', status: { type: 'idle' } } });

    expect(t.get('ses_1')?.state).toBe('idle');
  });

  // A session that is busy may well be mid-tool, so busy must not clear it.
  it('survives the session merely being busy', () => {
    const t = started();
    t.toolStarted({ sessionID: 'ses_1', tool: 'write', callID: 'c1' });

    t.apply({ type: 'session.status', properties: { sessionID: 'ses_1', status: { type: 'busy' } } });

    expect(t.get('ses_1')?.state).toBe('running_tool');
  });

  // An outstanding permission request is why a session can sit idle with
  // work pending, and it outranks idle either way.
  it('does not clear a pending permission', () => {
    const t = started();
    t.apply(askedBash);
    t.apply({ type: 'session.idle', properties: { sessionID: 'ses_1' } });

    expect(t.get('ses_1')?.state).toBe('waiting_input');
  });
});

describe('robustness', () => {
  it('ignores malformed events without throwing', () => {
    const t = tracker();
    for (const event of [null, undefined, 42, 'nonsense', {}, { type: 'session.created' }, { type: 'unknown.event' }]) {
      expect(() => t.apply(event)).not.toThrow();
    }
  });

  // A create missing required fields is not a session birddog can describe.
  it('ignores a create with an incomplete payload', () => {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info: { id: 'ses_1' } } });

    expect(t.get('ses_1')).toBeUndefined();
  });

  it('tracks several sessions independently', () => {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    t.apply({ type: 'session.created', properties: { info: { ...info, id: 'ses_2', slug: 'other' } } });

    t.apply(askedBash);

    expect(t.get('ses_1')?.state).toBe('waiting_input');
    expect(t.get('ses_2')?.state).not.toBe('waiting_input');
  });
});

// A plugin does not always see a session begin. muster creates the session
// and attaches a terminal, so by the time the plugin loads `session.created`
// has already fired — and a tracker that learns only from events knows about
// nothing, leaving the session invisible for its whole life.
//
// This was not hypothetical: on the first real muster-launched run the plugin
// logged a clean startup and then recorded nothing at all.
describe('sessions that already existed', () => {
  it('notices an event naming a session it does not know', () => {
    const t = tracker();
    t.apply({ type: 'session.idle', properties: { sessionID: 'ses_unknown' } });

    expect(t.takeUnknown()).toEqual(['ses_unknown']);
  });

  // Taking the list clears it: the plugin is about to go and resolve them,
  // and asking again on the next event must not repeat work already done.
  it('reports each unknown session once', () => {
    const t = tracker();
    t.apply({ type: 'session.idle', properties: { sessionID: 'ses_unknown' } });
    t.takeUnknown();

    expect(t.takeUnknown()).toEqual([]);
  });

  it('does not report a session it already tracks', () => {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    t.apply({ type: 'session.idle', properties: { sessionID: 'ses_1' } });

    expect(t.takeUnknown()).toEqual([]);
  });

  it('adopts a session it never saw created', () => {
    const t = tracker();
    t.adoptOne({ id: 'ses_existing', slug: 'existing', title: 'Already running',
                 directory: '/work/api', version: '1.18.31' });

    const s = t.get('ses_existing');
    expect(s?.slug).toBe('existing');
    expect(s?.directory).toBe('/work/api');
  });

  it('then tracks it like any other', () => {
    const t = tracker();
    t.adoptOne({ id: 'ses_existing', slug: 'x', title: 'x', directory: '/w', version: '1.18.31' });
    t.apply({ type: 'session.idle', properties: { sessionID: 'ses_existing' } });

    expect(t.get('ses_existing')?.state).toBe('idle');
  });

  // Adoption is driven by events this process emitted, so it can never pull
  // in opencode's session history — which on a well-used machine is a
  // hundred sessions that ended days ago. Republishing those under this
  // process's pid and a fresh heartbeat is exactly how birddog decides
  // something is live, and every one of them would read as running.
  it('does not disturb a session it is already tracking', () => {
    const t = tracker();
    t.apply({ type: 'session.created', properties: { info } });
    t.apply({ type: 'permission.asked', properties: {
      id: 'per_1', sessionID: 'ses_1', permission: 'bash', metadata: { command: 'ls' } } });

    t.adoptOne({ id: 'ses_1', slug: 'auth-refactor', title: 'Auth refactor',
                 directory: '/work/api', version: '1.18.31' });

    expect(t.get('ses_1')?.state).toBe('waiting_input');
  });

  it('ignores an entry it cannot describe', () => {
    const t = tracker();
    t.adoptOne({ id: 'ses_partial' });
    t.adoptOne(null);
    t.adoptOne('nonsense');

    expect(t.all()).toHaveLength(0);
  });
});
