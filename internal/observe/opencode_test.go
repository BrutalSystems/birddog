package observe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/policy"
)

func opencodeTarget(sessionID string) config.Target {
	return config.Target{
		ID: "worker-1", Provider: "opencode",
		Attachment: config.Attachment{Kind: "existing-session", SessionID: sessionID},
	}
}

func writeRecord(t *testing.T, dir string, rec map[string]any) {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	id, _ := rec["session_id"].(string)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}
}

func record(state string) map[string]any {
	return map[string]any{
		"session_id": "ses_1", "slug": "auth", "title": "Auth",
		"directory": "/work/api", "opencode_version": "1.18.31",
		"state": state, "pid": os.Getpid(), "plugin_version": "0.1.0",
		"last_activity_at": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"updated_at":       time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func opencodeObserver(dir string) *Opencode {
	return &Opencode{
		Dir:         dir,
		ProcessLive: func(int) bool { return true },
	}
}

func TestOpencodeObserverReadsPublishedState(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, record("active"))

	got, err := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusActive || !got.StatusKnown || !got.Live {
		t.Errorf("observation = %+v", got.Observation)
	}
}

func TestOpencodeObserverMapsEveryPublishedState(t *testing.T) {
	for _, state := range []string{
		policy.StatusActive, policy.StatusIdle, policy.StatusWaitingInput, policy.StatusRunningTool,
	} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			writeRecord(t, dir, record(state))

			got, _ := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
			if got.Status != state || !got.StatusKnown {
				t.Errorf("Status = %q known=%v, want %q", got.Status, got.StatusKnown, state)
			}
		})
	}
}

// The payoff for the plugin: opencode is the only provider where birddog can
// see a permission request, so it is the only one that reports anything other
// than "unavailable".
func TestOpencodeReportsAnObservedPermissionRequest(t *testing.T) {
	dir := t.TempDir()
	rec := record(policy.StatusWaitingInput)
	rec["pending_permission"] = map[string]any{"id": "p1", "asked_at": "2026-09-21T12:00:00Z"}
	writeRecord(t, dir, rec)

	got, _ := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if got.InputRequestVisibility != policy.VisibilityObserved {
		t.Errorf("InputRequestVisibility = %q, want observed", got.InputRequestVisibility)
	}
}

// Looked for and absent is a different answer from cannot look, and the
// plugin is what makes the first one available.
func TestOpencodeReportsNoRequestAsNotObserved(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, record(policy.StatusActive))

	got, _ := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if got.InputRequestVisibility != policy.VisibilityNotObserved {
		t.Errorf("InputRequestVisibility = %q, want not_observed", got.InputRequestVisibility)
	}
}

// Without a record, birddog cannot look at all — so it must not claim the
// stronger answer.
func TestOpencodeWithoutAPluginReportsVisibilityUnavailable(t *testing.T) {
	dir := t.TempDir()

	got, err := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if err == nil {
		t.Fatal("Observe = nil error for a session with no record")
	}
	if got.InputRequestVisibility != policy.VisibilityUnavailable {
		t.Errorf("InputRequestVisibility = %q, want unavailable", got.InputRequestVisibility)
	}
	if got.Status == policy.StatusExited {
		t.Error("a missing record was reported as an exit — the plugin may simply not be installed")
	}
}

// A process that died leaves its last record behind. The heartbeat is what
// distinguishes a quiet session from a dead one.
func TestOpencodeStaleHeartbeatIsNotLive(t *testing.T) {
	dir := t.TempDir()
	rec := record(policy.StatusActive)
	rec["updated_at"] = time.Now().Add(-10 * time.Minute).UTC().Format("2006-01-02T15:04:05Z")
	writeRecord(t, dir, rec)

	got, _ := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if got.Live {
		t.Error("Live = true for a record whose heartbeat stopped ten minutes ago")
	}
}

// Criterion 7 again, by a different route: the pid in a stale record may
// since belong to something else entirely, so a live pid alone proves nothing.
func TestOpencodeLivePIDWithAStaleHeartbeatIsStillNotLive(t *testing.T) {
	dir := t.TempDir()
	rec := record(policy.StatusActive)
	rec["updated_at"] = time.Now().Add(-10 * time.Minute).UTC().Format("2006-01-02T15:04:05Z")
	writeRecord(t, dir, rec)

	o := &Opencode{Dir: dir, ProcessLive: func(int) bool { return true }} // pid reused
	got, _ := o.Observe(opencodeTarget("ses_1"))
	if got.Live {
		t.Error("Live = true on a reused pid with a dead heartbeat")
	}
}

func TestOpencodeDeadProcessIsNotLive(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, record(policy.StatusActive))

	o := &Opencode{Dir: dir, ProcessLive: func(int) bool { return false }}
	got, _ := o.Observe(opencodeTarget("ses_1"))
	if got.Live {
		t.Error("Live = true though the writing process is gone")
	}
}

// The record's identity must change when the process behind it does.
func TestOpencodeIdentityIncludesTheWritingProcess(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, record(policy.StatusActive))
	first, _ := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))

	rec := record(policy.StatusActive)
	rec["pid"] = os.Getpid() + 1
	writeRecord(t, dir, rec)
	second, _ := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))

	if first.SessionIdentity == second.SessionIdentity {
		t.Error("a session republished by another process kept its identity")
	}
}

func TestOpencodeUsesTheSessionsOwnActivityStamp(t *testing.T) {
	dir := t.TempDir()
	rec := record(policy.StatusActive)
	when := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	rec["last_activity_at"] = when.Format("2006-01-02T15:04:05Z")
	writeRecord(t, dir, rec)

	got, _ := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if !got.LastActivityAt.Equal(when) {
		t.Errorf("LastActivityAt = %v, want the session's own stamp %v", got.LastActivityAt, when)
	}
}

// A record birddog cannot parse describes no session it can report.
func TestOpencodeIgnoresAnUnreadableRecord(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ses_1.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if err == nil {
		t.Fatal("Observe = nil error for an unreadable record")
	}
	if got.StatusKnown {
		t.Error("an unreadable record produced a confident observation")
	}
}

func TestOpencodeDoesNotGuessAtAnUnknownState(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, record("something-new"))

	got, _ := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if got.StatusKnown {
		t.Error("StatusKnown = true for a state this build does not recognise")
	}
	if !got.Live {
		t.Error("Live = false though the plugin is publishing and its process is up")
	}
}

// The bound belongs to the reader as well as the writer. A record written by a
// plugin version that did not bound its subject — an older one still resolved
// from opencode's package cache — must not put an unbounded value into the
// durable log.
func TestOpencodeBoundsAnOverlongRequestSubjectFromTheRecord(t *testing.T) {
	dir := t.TempDir()
	rec := record("waiting_input")
	rec["pending_permission"] = map[string]any{
		"id": "p1", "asked_at": "2026-09-21T12:00:00Z",
		"type": "bash", "detail": strings.Repeat("x", maxDetail*2),
	}
	writeRecord(t, dir, rec)

	got, err := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if n := len([]rune(got.InputRequestDetail)); n > maxDetail+1 {
		t.Errorf("InputRequestDetail = %d runes, want it bounded to %d plus the marker", n, maxDetail)
	}
	if !strings.HasSuffix(got.InputRequestDetail, "…") {
		t.Errorf("InputRequestDetail = %q, want the truncation marker", got.InputRequestDetail)
	}
}

func TestOpencodeRecordReportsActivityResolution(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, record("idle"))

	got, err := opencodeObserver(dir).Observe(opencodeTarget("ses_1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.ActivityResolution != policy.ResolutionActivity {
		t.Errorf("ActivityResolution = %q, want activity: the plugin restamps at tool boundaries",
			got.ActivityResolution)
	}
}

func TestOpencodeWithoutARecordReportsUnavailableResolution(t *testing.T) {
	// No record written: the plugin is the only thing that publishes one, so
	// its absence means nothing is watching this session's activity.
	got, _ := opencodeObserver(t.TempDir()).Observe(opencodeTarget("ses_1"))
	if got.ActivityResolution != policy.ResolutionUnavailable {
		t.Errorf("ActivityResolution = %q, want unavailable", got.ActivityResolution)
	}
}
