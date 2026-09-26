package claudeinbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSession(t *testing.T, dir string, pid int, sessionID, socket string) {
	t.Helper()
	rec := map[string]any{
		"pid": pid, "sessionId": sessionID, "messagingSocketPath": socket,
		"procStart": "Mon Sep 21 15:53:11 2026", "pidDomain": "darwin", "status": "idle",
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, itoa(pid)+".json"), b, 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}
}

func writeKey(t *testing.T, dir string, pid int, token string) {
	t.Helper()
	body := map[string]string{"peerToken": token, "procStart": "Mon Sep 21 15:53:11 2026", "pidDomain": "darwin"}
	b, _ := json.Marshal(body)
	name := itoa(pid) + ".deadbeefcafe0123.key"
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestResolveFindsTheSocketAndToken(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, 41207, "session-abc", "/tmp/cc-socks/41207.sock")
	writeKey(t, dir, 41207, "tok-123")

	got, err := ResolveSession(dir, "session-abc")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if got.SocketPath != "/tmp/cc-socks/41207.sock" {
		t.Errorf("SocketPath = %q", got.SocketPath)
	}
	if got.PeerToken != "tok-123" {
		t.Errorf("PeerToken not read")
	}
	if got.ProcStart == "" || got.PIDDomain == "" {
		t.Errorf("recipient = %+v, want the auth fields populated", got)
	}
}

// The orchestrator restarting changes its pid, socket and token, so the
// recipient is resolved for each delivery rather than once.
func TestResolveFollowsASessionToItsNewProcess(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, 41207, "session-abc", "/tmp/cc-socks/41207.sock")
	writeKey(t, dir, 41207, "old-token")

	first, err := ResolveSession(dir, "session-abc")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}

	// The session comes back under a new pid.
	os.Remove(filepath.Join(dir, "41207.json"))
	os.Remove(filepath.Join(dir, "41207.deadbeefcafe0123.key"))
	writeSession(t, dir, 55555, "session-abc", "/tmp/cc-socks/55555.sock")
	writeKey(t, dir, 55555, "new-token")

	second, err := ResolveSession(dir, "session-abc")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if second.SocketPath == first.SocketPath || second.PeerToken == first.PeerToken {
		t.Error("resolution did not follow the session to its new process")
	}
}

func TestResolveReportsAnUnknownSession(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, 41207, "session-abc", "/tmp/cc-socks/41207.sock")

	_, err := ResolveSession(dir, "never-existed")
	if err == nil {
		t.Fatal("ResolveSession = nil error for a session that is not there")
	}
	if !strings.Contains(err.Error(), "never-existed") {
		t.Errorf("error = %v, want it to name the session asked for", err)
	}
}

// Without the token birddog cannot authenticate, and sending anyway would
// produce a silent non-delivery.
func TestResolveReportsAMissingToken(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, 41207, "session-abc", "/tmp/cc-socks/41207.sock")
	// no key file

	if _, err := ResolveSession(dir, "session-abc"); err == nil {
		t.Error("ResolveSession = nil error with no peer token available")
	}
}

func TestFactoryRequiresASessionID(t *testing.T) {
	if _, err := newConnector(map[string]string{}); err == nil {
		t.Error("newConnector = nil error with no session_id")
	}
}

// An unrecognised setting is a typo far more often than an intention, and
// ignoring it means alerts go somewhere the operator did not choose.
func TestFactoryRejectsUnknownSettings(t *testing.T) {
	_, err := newConnector(map[string]string{"session_id": "abc", "sesion_id": "typo"})
	if err == nil {
		t.Fatal("newConnector = nil error for an unknown setting")
	}
	if !strings.Contains(err.Error(), "sesion_id") {
		t.Errorf("error = %v, want it to name the offending setting", err)
	}
}

func TestFactoryAcceptsASessionID(t *testing.T) {
	c, err := newConnector(map[string]string{"session_id": "abc"})
	if err != nil {
		t.Fatalf("newConnector: %v", err)
	}
	if c.Name() != Kind {
		t.Errorf("Name = %q", c.Name())
	}
}
