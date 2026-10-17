package observe

import (
	"testing"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
)

func attachedTarget(sessionID string, pid int, procStart string) config.Target {
	t := claudeTarget(sessionID)
	t.Attachment.PID = pid
	t.Attachment.ProcStart = procStart
	return t
}

// /clear mints a new session id in the same process. The target follows the
// process rather than reporting the id it was given as lost.
func TestClaudeFollowsASessionClearedInTheSameProcess(t *testing.T) {
	o := &Claude{
		List: listing(claude.Session{
			SessionID: "s2", PID: 100, ProcStart: "P", Status: "idle", Live: true, StatusUpdatedAt: t0,
		}),
		SameProcess: func(int, string) bool { return true },
	}

	got, err := o.Observe(attachedTarget("s1", 100, "P"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	want := monitor.Follow{FromSessionID: "s1", ToSessionID: "s2", PID: 100, ProcStart: "P", Via: monitor.FollowViaProcess}
	if got.Follow == nil || *got.Follow != want {
		t.Fatalf("Follow = %+v, want %+v", got.Follow, want)
	}
	if !got.Live || got.Status != policy.StatusIdle || !got.StatusKnown {
		t.Errorf("sighting = live %v status %q known %v, want the new session's live idle state", got.Live, got.Status, got.StatusKnown)
	}
	if got.SessionIdentity != "s2@P" {
		t.Errorf("SessionIdentity = %q, want s2@P", got.SessionIdentity)
	}
}

// A target configured with only a session id still has a process behind it,
// learned from the passes that saw it.
func TestClaudeFollowsUsingTheProcessLastSeen(t *testing.T) {
	sessions := []claude.Session{{SessionID: "s1", PID: 100, ProcStart: "P", Status: "busy", Live: true}}
	o := &Claude{List: func() ([]claude.Session, error) { return sessions, nil }}
	target := claudeTarget("s1")

	if got, _ := o.Observe(target); got.Follow != nil {
		t.Fatalf("first pass followed %+v while the configured id was present", got.Follow)
	}

	sessions = []claude.Session{{SessionID: "s2", PID: 100, ProcStart: "P", Status: "busy", Live: true}}
	got, err := o.Observe(target)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Follow == nil || got.Follow.ToSessionID != "s2" {
		t.Fatalf("Follow = %+v, want a follow to s2", got.Follow)
	}
}

// A pid alone is reused by the kernel. A different start time is a different
// process, and following onto it would watch a stranger.
func TestClaudeDoesNotFollowAReusedPID(t *testing.T) {
	o := &Claude{
		List: listing(claude.Session{
			SessionID: "s2", PID: 100, ProcStart: "Q", Status: "idle", Live: true,
		}),
		SameProcess: func(int, string) bool { return false },
	}

	got, _ := o.Observe(attachedTarget("s1", 100, "P"))
	if got.Follow != nil {
		t.Fatalf("Follow = %+v onto a different process", got.Follow)
	}
	if got.Status != policy.StatusExited {
		t.Errorf("Status = %q, want exited: the recorded process is gone", got.Status)
	}
}

// A registry record that does not answer proves only that something once
// wrote it.
func TestClaudeDoesNotFollowARecordThatIsNotLive(t *testing.T) {
	o := &Claude{
		List: listing(claude.Session{
			SessionID: "s2", PID: 100, ProcStart: "P", Status: "idle", Live: false,
		}),
		SameProcess: func(int, string) bool { return true },
	}

	got, _ := o.Observe(attachedTarget("s1", 100, "P"))
	if got.Follow != nil {
		t.Fatalf("Follow = %+v onto a record that is not live", got.Follow)
	}
}

// A target pointed at another session — by watch sync, or removed and added
// again — must not inherit the process learned for the old one. Otherwise
// "the new id is not there, and the old process now answers as the old id"
// reads as a follow straight back to the worker it was moved away from.
func TestARetargetedWatchIsNotFollowedBackToTheOldSession(t *testing.T) {
	sessions := []claude.Session{{SessionID: "A", PID: 100, ProcStart: "P", Status: "busy", Live: true}}
	o := &Claude{
		List:        func() ([]claude.Session, error) { return sessions, nil },
		SameProcess: func(int, string) bool { return true },
	}

	if got, _ := o.Observe(claudeTarget("A")); got.Follow != nil {
		t.Fatalf("followed %+v while A was present", got.Follow)
	}

	// The same target id, now pointed at B, which has not appeared.
	got, _ := o.Observe(claudeTarget("B"))
	if got.Follow != nil {
		t.Fatalf("Follow = %+v: the retargeted watch was followed back to A", got.Follow)
	}

	// A genuine /clear of the session the process was learned for is still
	// followed.
	sessions = []claude.Session{{SessionID: "A2", PID: 100, ProcStart: "P", Status: "busy", Live: true}}
	if got, _ := o.Observe(claudeTarget("A")); got.Follow == nil || got.Follow.ToSessionID != "A2" {
		t.Errorf("Follow = %+v, want A followed to A2", got.Follow)
	}
}
