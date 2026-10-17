package daemon

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/observe"
	"github.com/BrutalSystems/birddog/internal/observe/transcript"
)

// registry is a session registry a test can rewrite between passes.
type registry struct {
	mu       sync.Mutex
	sessions []claude.Session
}

func (r *registry) set(ss ...claude.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = ss
}

func (r *registry) list() ([]claude.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]claude.Session(nil), r.sessions...), nil
}

func liveAt(sessionID string) claude.Session {
	return claude.Session{SessionID: sessionID, PID: 4100, ProcStart: "P", Status: "idle", Live: true}
}

// startClaude runs an instance over the real Claude observer, wrapped as it
// is in production, reading a registry the test controls.
func startClaude(t *testing.T, reg *registry, sessionID string) Options {
	t.Helper()
	obs := transcript.Watch(&observe.Claude{
		List:        reg.list,
		SameProcess: func(int, string) bool { return true },
	}, nil)
	_, opts := startWith(t, nil, func(o *Options) {
		o.Config = &config.Config{SchemaVersion: 1, Name: "forget", Targets: []config.Target{claudeTarget(sessionID)}}
		o.Observers = map[string]monitor.Observer{"claude": obs}
	})
	waitFor(t, "the session to be observed", func() bool {
		return targets(t, opts.SocketPath)[0].PID == 4100
	})
	return opts
}

func claudeTarget(sessionID string) config.Target {
	return config.Target{
		ID: "worker", Provider: "claude",
		Attachment: config.Attachment{Kind: "existing-session", SessionID: sessionID},
		Policy:     config.FillDefaults(config.Policy{}),
	}
}

func claudeSpec(sessionID string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id": "worker", "provider": "claude",
		"attachment": {"kind": "existing-session", "session_id": %q}}`, sessionID))
}

func claudeLedger(sessionID string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"schema_version": 1, "name": "forget", "targets": [%s]}`, claudeSpec(sessionID)))
}

// passAfter waits until the target has been observed again after a cursor.
func passAfter(t *testing.T, socket string, cursor int64) {
	t.Helper()
	waitFor(t, "a pass after the change", func() bool {
		var res EventsResult
		_ = ipc.Call(socket, "events", EventsParams{After: cursor}, &res)
		for _, e := range res.Events {
			if e.TargetID == "worker" && e.Type == "observation" {
				return true
			}
		}
		return false
	})
}

func cursorNow(t *testing.T, socket string) int64 {
	t.Helper()
	var st StatusResult
	if err := callStatus(socket, &st); err != nil {
		t.Fatal(err)
	}
	return st.Cursor
}

func noFollowSince(t *testing.T, socket string, cursor int64) {
	t.Helper()
	var res EventsResult
	if err := ipc.Call(socket, "events", EventsParams{After: cursor, Limit: 1000}, &res); err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Events {
		if e.Type == monitor.EventSessionFollowed {
			t.Errorf("session_followed %s: a watch added again followed a process learned before it", e.Evidence)
		}
	}
}

// Removing a watch and adding it again, for the same session, starts fresh.
// The process learned before the removal does not survive it, so a session
// that has since changed id in that process is not followed: continuity comes
// only from a follow already recorded.
func TestReAddingTheSameSessionStartsFresh(t *testing.T) {
	reg := &registry{}
	reg.set(liveAt("A"))
	opts := startClaude(t, reg, "A")

	if err := ipc.Call(opts.SocketPath, "watch.remove", WatchRemoveParams{TargetID: "worker"}, &WatchResult{}); err != nil {
		t.Fatalf("watch.remove: %v", err)
	}
	reg.set(liveAt("A2")) // the same process, now under a new id
	cursor := cursorNow(t, opts.SocketPath)
	if err := ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Spec: claudeSpec("A")}, &WatchResult{}); err != nil {
		t.Fatalf("watch.add: %v", err)
	}
	passAfter(t, opts.SocketPath, cursor)

	noFollowSince(t, opts.SocketPath, cursor)
	if got := targets(t, opts.SocketPath)[0].AttachedSessionID; got != "A" {
		t.Errorf("attached_session_id = %q, want A as configured", got)
	}
}

// A watch synced from A to B and back to A starts fresh at each change.
func TestSyncRetargetingAwayAndBackStartsFresh(t *testing.T) {
	reg := &registry{}
	reg.set(liveAt("A"))
	opts := startClaude(t, reg, "A")
	start := cursorNow(t, opts.SocketPath)

	if res := syncWith(t, opts.SocketPath, WatchSyncParams{Config: claudeLedger("B")}); len(res.Updated) != 1 {
		t.Fatalf("sync to B = %+v, want worker updated", res)
	}
	passAfter(t, opts.SocketPath, cursorNow(t, opts.SocketPath))

	reg.set(liveAt("A2"))
	cursor := cursorNow(t, opts.SocketPath)
	if res := syncWith(t, opts.SocketPath, WatchSyncParams{Config: claudeLedger("A")}); len(res.Updated) != 1 {
		t.Fatalf("sync back to A = %+v, want worker updated", res)
	}
	passAfter(t, opts.SocketPath, cursor)

	noFollowSince(t, opts.SocketPath, start)
	if got := targets(t, opts.SocketPath)[0].AttachedSessionID; got != "A" {
		t.Errorf("attached_session_id = %q, want A as configured", got)
	}
}

// The control: left alone, the same /clear is followed.
func TestAnUnchangedWatchStillFollowsAClear(t *testing.T) {
	reg := &registry{}
	reg.set(liveAt("A"))
	opts := startClaude(t, reg, "A")

	reg.set(liveAt("A2"))
	waitFor(t, "the follow", func() bool { return targets(t, opts.SocketPath)[0].AttachedSessionID == "A2" })
}
