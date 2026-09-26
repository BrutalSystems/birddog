package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// record is the shape Claude Code 2.1.267 writes to
// ~/.claude/sessions/<pid>.json. See docs/integration-findings.md §2.
type record map[string]any

func writeRecord(t *testing.T, dir string, pid int, r record) {
	t.Helper()
	r["pid"] = pid
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	name := filepath.Join(dir, fileNameFor(pid))
	if err := os.WriteFile(name, b, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func fileNameFor(pid int) string {
	return strconv.Itoa(pid) + ".json"
}

// alwaysLive stands in for a connect probe of the session's unix socket, so
// tests need no real sockets.
func alwaysLive(string) bool { return true }

// sameProc stands in for process-identity verification, so tests need no real
// processes. Liveness requires both this and the socket probe.
func sameProc(int, string) bool { return true }

func TestListParsesRegistryRecord(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 41207, record{
		"sessionId":           "11111111-2222-3333-4444-555555555555",
		"cwd":                 "/work/repo",
		"procStart":           "Thu Sep 17 22:11:09 2026",
		"version":             "2.1.267",
		"kind":                "interactive",
		"entrypoint":          "cli",
		"messagingSocketPath": "/tmp/cc-socks/41207.sock",
		"name":                "repo-98",
		"status":              "idle",
		"updatedAt":           int64(1789683614652),
		"statusUpdatedAt":     int64(1789683614652),
	})

	got, err := List(Params{RegistryDir: dir, Probe: alwaysLive, SameProcess: sameProc})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}

	s := got[0]
	if s.SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("SessionID = %q", s.SessionID)
	}
	if s.PID != 41207 {
		t.Errorf("PID = %d, want 41207", s.PID)
	}
	if s.ProcStart != "Thu Sep 17 22:11:09 2026" {
		t.Errorf("ProcStart = %q", s.ProcStart)
	}
	if s.CWD != "/work/repo" {
		t.Errorf("CWD = %q", s.CWD)
	}
	if s.Name != "repo-98" {
		t.Errorf("Name = %q", s.Name)
	}
	if s.Status != "idle" {
		t.Errorf("Status = %q", s.Status)
	}
	if s.Kind != "interactive" {
		t.Errorf("Kind = %q", s.Kind)
	}
	if s.Entrypoint != "cli" {
		t.Errorf("Entrypoint = %q", s.Entrypoint)
	}
	if s.Version != "2.1.267" {
		t.Errorf("Version = %q", s.Version)
	}
	if s.SocketPath != "/tmp/cc-socks/41207.sock" {
		t.Errorf("SocketPath = %q", s.SocketPath)
	}
	want := time.UnixMilli(1789683614652)
	if !s.StatusUpdatedAt.Equal(want) {
		t.Errorf("StatusUpdatedAt = %v, want %v", s.StatusUpdatedAt, want)
	}
}

// The registry directory also holds <pid>.<hash>.key files carrying a
// peerToken. They are valid JSON, so a listing that globs *.json would parse
// one into a bogus session — and would be reading credential material it has
// no business touching.
func TestListIgnoresPeerTokenKeyFiles(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 41207, record{"sessionId": "real-session"})

	keyFile := filepath.Join(dir, "41207.deadbeefcafe.key")
	body := []byte(`{"peerToken":"sekrit","procStart":"Thu Sep 17 22:11:09 2026"}`)
	if err := os.WriteFile(keyFile, body, 0o600); err != nil {
		t.Fatalf("write key fixture: %v", err)
	}

	got, err := List(Params{RegistryDir: dir, Probe: alwaysLive, SameProcess: sameProc})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1 (the .key file must be ignored)", len(got))
	}
	if got[0].SessionID != "real-session" {
		t.Errorf("SessionID = %q, want the record not the key file", got[0].SessionID)
	}
}

// A stray file that is neither <pid>.json nor a key file must not become a
// session either.
func TestListIgnoresNonRegistryJSON(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 41207, record{"sessionId": "real-session"})
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"sessionId":"nope"}`), 0o600); err != nil {
		t.Fatalf("write stray fixture: %v", err)
	}

	got, err := List(Params{RegistryDir: dir, Probe: alwaysLive, SameProcess: sameProc})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1 (only <pid>.json is a registry record)", len(got))
	}
}

// The registry record can outlive the process that wrote it, so the socket is
// the liveness test. A dead session's record must not be reported as current.
func TestListMarksSessionNotLiveWhenSocketRefuses(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 41207, record{
		"sessionId":           "gone",
		"status":              "active",
		"statusUpdatedAt":     int64(1789683614652),
		"messagingSocketPath": "/tmp/cc-socks/41207.sock",
	})

	got, err := List(Params{RegistryDir: dir, Probe: func(string) bool { return false }, SameProcess: sameProc})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].Live {
		t.Error("Live = true, want false when the socket refuses")
	}
}

func TestListMarksSessionLiveWhenSocketAnswers(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 41207, record{"sessionId": "here", "messagingSocketPath": "/tmp/cc-socks/41207.sock"})

	got, err := List(Params{RegistryDir: dir, Probe: alwaysLive, SameProcess: sameProc})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !got[0].Live {
		t.Error("Live = false, want true when the socket answers")
	}
}

// Losing observation must not destroy what was last observed: the handoff
// requires last-known state preserved with its timestamp, marked stale rather
// than silently dropped or presented as current.
func TestNotLiveSessionRetainsLastKnownStatusAndTimestamp(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 41207, record{
		"sessionId":           "gone",
		"status":              "active",
		"statusUpdatedAt":     int64(1789683614652),
		"messagingSocketPath": "/tmp/cc-socks/41207.sock",
	})

	got, err := List(Params{RegistryDir: dir, Probe: func(string) bool { return false }, SameProcess: sameProc})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	s := got[0]
	if s.Status != "active" {
		t.Errorf("Status = %q, want the last-known %q preserved", s.Status, "active")
	}
	if !s.StatusUpdatedAt.Equal(time.UnixMilli(1789683614652)) {
		t.Errorf("StatusUpdatedAt = %v, want the last-known timestamp preserved", s.StatusUpdatedAt)
	}
	if s.StatusIsCurrent() {
		t.Error("StatusIsCurrent() = true, want false — a dead session's status is stale, not a current fact")
	}
}

// Criterion 7: a socket file can linger after the process that bound it is
// gone, and the PID can meanwhile be reused. Process identity, not the socket
// alone, decides whether this is still the recorded session.
func TestListMarksSessionNotLiveWhenPIDWasReused(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 41207, record{
		"sessionId":           "original",
		"procStart":           "Thu Sep 17 22:11:09 2026",
		"messagingSocketPath": "/tmp/cc-socks/41207.sock",
	})

	got, err := List(Params{
		RegistryDir: dir,
		Probe:       alwaysLive,
		SameProcess: func(int, string) bool { return false }, // PID now holds someone else
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].Live {
		t.Error("Live = true, want false — the PID belongs to a different process now")
	}
}

// The replacement must not be silently adopted: the session keeps its own
// identity and is reported as gone, not rewritten to describe the new process.
func TestReusedPIDDoesNotAdoptTheReplacementProcess(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 41207, record{
		"sessionId":           "original",
		"procStart":           "Thu Sep 17 22:11:09 2026",
		"messagingSocketPath": "/tmp/cc-socks/41207.sock",
	})

	got, err := List(Params{
		RegistryDir: dir,
		Probe:       alwaysLive,
		SameProcess: func(int, string) bool { return false },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	s := got[0]
	if s.SessionID != "original" {
		t.Errorf("SessionID = %q, want the recorded session preserved", s.SessionID)
	}
	if s.ProcStart != "Thu Sep 17 22:11:09 2026" {
		t.Errorf("ProcStart = %q, want the recorded start time preserved", s.ProcStart)
	}
	if s.StatusIsCurrent() {
		t.Error("StatusIsCurrent() = true, want false for a session whose process is gone")
	}
}

// A live socket plus a matching process is the only combination that counts.
func TestListMarksSessionLiveOnlyWhenSocketAndProcessAgree(t *testing.T) {
	cases := []struct {
		name     string
		socket   bool
		sameProc bool
		wantLive bool
	}{
		{"socket answers, process matches", true, true, true},
		{"socket refuses, process matches", false, true, false},
		{"socket answers, process replaced", true, false, false},
		{"socket refuses, process replaced", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRecord(t, dir, 41207, record{
				"sessionId":           "s",
				"procStart":           "Thu Sep 17 22:11:09 2026",
				"messagingSocketPath": "/tmp/cc-socks/41207.sock",
			})
			got, err := List(Params{
				RegistryDir: dir,
				Probe:       func(string) bool { return tc.socket },
				SameProcess: func(int, string) bool { return tc.sameProc },
			})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got[0].Live != tc.wantLive {
				t.Errorf("Live = %v, want %v", got[0].Live, tc.wantLive)
			}
		})
	}
}

func TestDefaultRegistryDirIsUnderTheUsersClaudeHome(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	got, err := DefaultRegistryDir()
	if err != nil {
		t.Fatalf("DefaultRegistryDir: %v", err)
	}
	if want := "/home/someone/.claude/sessions"; got != want {
		t.Errorf("DefaultRegistryDir() = %q, want %q", got, want)
	}
}
