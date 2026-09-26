package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func store(t *testing.T) *Store {
	t.Helper()
	return &Store{Dir: t.TempDir()}
}

func event(kind string, fields map[string]any) map[string]any {
	e := map[string]any{"hook_event_name": kind, "session_id": "ses-1", "cwd": "/work/api"}
	for k, v := range fields {
		e[k] = v
	}
	return e
}

func TestToolStartIsRecorded(t *testing.T) {
	s := store(t)
	if err := s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := s.Read("ses-1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.RunningTools) != 1 || got.RunningTools[0].Name != "Bash" {
		t.Errorf("RunningTools = %+v", got.RunningTools)
	}
}

func TestToolCompletionClearsIt(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))
	_ = s.Apply(event("PostToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))

	got, _ := s.Read("ses-1")
	if len(got.RunningTools) != 0 {
		t.Errorf("RunningTools = %+v, want none", got.RunningTools)
	}
}

// Claude runs tools in parallel, and each hook process writes independently.
// Nothing may require them to coordinate.
func TestParallelToolsAreTrackedIndependently(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Read", "tool_use_id": "t2"}))
	_ = s.Apply(event("PostToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))

	got, _ := s.Read("ses-1")
	if len(got.RunningTools) != 1 || got.RunningTools[0].Name != "Read" {
		t.Errorf("RunningTools = %+v, want only Read still running", got.RunningTools)
	}
}

// A completion whose start was never seen must not leave anything behind.
func TestUnmatchedCompletionIsHarmless(t *testing.T) {
	s := store(t)
	if err := s.Apply(event("PostToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := s.Read("ses-1")
	if len(got.RunningTools) != 0 {
		t.Errorf("RunningTools = %+v", got.RunningTools)
	}
}

// The whole point of hooks for Claude Code: a permission request becomes
// visible, so input-request visibility stops being "unavailable".
func TestPermissionRequestIsRecorded(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))

	got, _ := s.Read("ses-1")
	if len(got.PendingPermissions) != 1 {
		t.Fatalf("PendingPermissions = %+v", got.PendingPermissions)
	}
	if got.PendingPermissions[0].Tool != "Bash" {
		t.Errorf("Tool = %q", got.PendingPermissions[0].Tool)
	}
}

// A request is answered when the tool runs, or when it is denied. Either way
// the session is no longer waiting on a human.
func TestPermissionResolvedByTheToolRunning(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))

	got, _ := s.Read("ses-1")
	if len(got.PendingPermissions) != 0 {
		t.Errorf("PendingPermissions = %+v, want cleared once the tool proceeded", got.PendingPermissions)
	}
}

func TestPermissionResolvedByDenial(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))
	_ = s.Apply(event("PermissionDenied", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))

	got, _ := s.Read("ses-1")
	if len(got.PendingPermissions) != 0 {
		t.Errorf("PendingPermissions = %+v, want cleared by the denial", got.PendingPermissions)
	}
}

// A turn ending is a turn ending. It is recorded as such and nothing more:
// birddog never reports it as work being finished.
func TestStopRecordsTheTurnEnding(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("Stop", nil))

	got, _ := s.Read("ses-1")
	if got.LastTurnEndedAt.IsZero() {
		t.Error("LastTurnEndedAt not recorded")
	}
}

// A stopped turn cannot still be running tools; the markers would otherwise
// survive a crash mid-tool and report work forever.
func TestStopClearsRunningTools(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))
	_ = s.Apply(event("Stop", nil))

	got, _ := s.Read("ses-1")
	if len(got.RunningTools) != 0 {
		t.Errorf("RunningTools = %+v, want cleared when the turn ended", got.RunningTools)
	}
}

func TestSessionEndRemovesEverything(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))
	_ = s.Apply(event("SessionEnd", nil))

	got, err := s.Read("ses-1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Present {
		t.Error("state survived the session ending")
	}
}

func TestEveryEventAdvancesActivity(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))

	got, _ := s.Read("ses-1")
	if time.Since(got.LastActivityAt) > time.Minute {
		t.Errorf("LastActivityAt = %v, want it fresh", got.LastActivityAt)
	}
}

// A hook that has not run leaves nothing, and that is reported as nothing
// rather than as a session with no pending requests.
func TestUnknownSessionReportsAbsence(t *testing.T) {
	s := store(t)
	got, err := s.Read("never-seen")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Present {
		t.Error("Present = true for a session no hook ever reported")
	}
}

// Sessions must not read each other's state.
func TestSessionsAreIsolated(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))
	other := event("PreToolUse", map[string]any{"tool_name": "Read", "tool_use_id": "t2"})
	other["session_id"] = "ses-2"
	_ = s.Apply(other)

	a, _ := s.Read("ses-1")
	b, _ := s.Read("ses-2")
	if len(a.PendingPermissions) != 1 || len(b.PendingPermissions) != 0 {
		t.Errorf("state leaked between sessions: %+v / %+v", a, b)
	}
}

// A session id is used as a path component, so it must not be able to name
// anything outside the store.
func TestSessionIDsCannotEscapeTheStore(t *testing.T) {
	s := store(t)
	for _, id := range []string{"../escape", "a/b", "", ".", ".."} {
		e := event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"})
		e["session_id"] = id
		if err := s.Apply(e); err == nil {
			t.Errorf("Apply accepted session id %q", id)
		}
	}
}

func TestToolUseIDsCannotEscapeTheStore(t *testing.T) {
	s := store(t)
	e := event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "../../escape"})
	if err := s.Apply(e); err == nil {
		t.Error("Apply accepted a tool_use_id containing a path traversal")
	}
}

// Markers outlive a crash. One older than the window is not evidence that a
// tool is still running an hour later.
func TestStaleMarkersAreIgnored(t *testing.T) {
	s := store(t)
	s.StaleAfter = 50 * time.Millisecond
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))

	time.Sleep(80 * time.Millisecond)
	got, _ := s.Read("ses-1")
	if len(got.RunningTools) != 0 {
		t.Errorf("RunningTools = %+v, want a stale marker ignored", got.RunningTools)
	}
}

func TestStateIsOwnerOnly(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"}))

	info, err := os.Stat(filepath.Join(s.Dir, "ses-1"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions = %04o, want 0700", perm)
	}
}

func TestUnrecognisedEventsAreIgnoredQuietly(t *testing.T) {
	s := store(t)
	for _, kind := range []string{"PreCompact", "WorktreeCreate", "InstructionsLoaded", "nonsense"} {
		if err := s.Apply(event(kind, nil)); err != nil {
			t.Errorf("Apply(%s) = %v, want an unrecognised event ignored", kind, err)
		}
	}
}

func TestEventWithoutASessionIsRejectedNotGuessed(t *testing.T) {
	s := store(t)
	e := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash"}
	if err := s.Apply(e); err == nil {
		t.Error("Apply accepted an event naming no session")
	}
}

func TestToolNamesAreRecordedVerbatim(t *testing.T) {
	s := store(t)
	_ = s.Apply(event("PreToolUse", map[string]any{"tool_name": "mcp__some-server__do_thing", "tool_use_id": "t1"}))

	got, _ := s.Read("ses-1")
	if len(got.RunningTools) != 1 || !strings.Contains(got.RunningTools[0].Name, "mcp__some-server") {
		t.Errorf("RunningTools = %+v", got.RunningTools)
	}
}

// A PermissionRequest carrying no tool_use_id is recorded under the tool name
// (toolID falls back to it). The PreToolUse that follows when the human
// approves carries a real id, so removing perm.<id> leaves perm.<tool_name>
// behind and the session keeps reporting a human it is no longer waiting on.
//
// Observed on Claude Code 2.1.274: AskUserQuestion does exactly this.
func TestApprovingARequestRecordedByNameClearsIt(t *testing.T) {
	s := store(t)

	if err := s.Apply(event("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion"})); err != nil {
		t.Fatalf("Apply PermissionRequest: %v", err)
	}
	if err := s.Apply(event("PreToolUse", map[string]any{
		"tool_name": "AskUserQuestion", "tool_use_id": "toolu_01ABC",
	})); err != nil {
		t.Fatalf("Apply PreToolUse: %v", err)
	}

	got, err := s.Read("ses-1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.PendingPermissions) != 0 {
		t.Errorf("PendingPermissions = %+v, want none — the tool ran, so the request was answered", got.PendingPermissions)
	}
}

// The same leak through the completion event, for a tool whose PreToolUse
// birddog never saw.
func TestCompletingARequestRecordedByNameClearsIt(t *testing.T) {
	s := store(t)

	if err := s.Apply(event("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion"})); err != nil {
		t.Fatalf("Apply PermissionRequest: %v", err)
	}
	if err := s.Apply(event("PostToolUse", map[string]any{
		"tool_name": "AskUserQuestion", "tool_use_id": "toolu_01ABC",
	})); err != nil {
		t.Fatalf("Apply PostToolUse: %v", err)
	}

	got, _ := s.Read("ses-1")
	if len(got.PendingPermissions) != 0 {
		t.Errorf("PendingPermissions = %+v, want none", got.PendingPermissions)
	}
}

// The backstop: a turn cannot end while a permission is outstanding, because
// the turn is what is blocked on it. Anything still recorded when the turn
// ends was answered — or abandoned, which is not waiting either.
func TestATurnEndingClearsAnOutstandingPermission(t *testing.T) {
	s := store(t)

	if err := s.Apply(event("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion"})); err != nil {
		t.Fatalf("Apply PermissionRequest: %v", err)
	}
	if err := s.Apply(event("Stop", nil)); err != nil {
		t.Fatalf("Apply Stop: %v", err)
	}

	got, _ := s.Read("ses-1")
	if len(got.PendingPermissions) != 0 {
		t.Errorf("PendingPermissions = %+v after the turn ended, want none", got.PendingPermissions)
	}
}

// A request that is genuinely outstanding must survive an unrelated tool
// running, or the fix would clear the signal it exists to carry.
func TestAnUnrelatedToolDoesNotClearAPendingPermission(t *testing.T) {
	s := store(t)

	if err := s.Apply(event("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_use_id": "t1"})); err != nil {
		t.Fatalf("Apply PermissionRequest: %v", err)
	}
	if err := s.Apply(event("PreToolUse", map[string]any{"tool_name": "Read", "tool_use_id": "t2"})); err != nil {
		t.Fatalf("Apply PreToolUse: %v", err)
	}

	got, _ := s.Read("ses-1")
	if len(got.PendingPermissions) != 1 {
		t.Errorf("PendingPermissions = %+v, want the Bash request still outstanding", got.PendingPermissions)
	}
}
