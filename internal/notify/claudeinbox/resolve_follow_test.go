package claudeinbox

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRecord(t *testing.T, dir, sessionID string) {
	t.Helper()
	record := `{"pid": 7, "sessionId": "` + sessionID + `", "procStart": "P", "messagingSocketPath": "/tmp/x.sock"}`
	if err := os.WriteFile(filepath.Join(dir, "7.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The orchestrator runs /clear: same process, new session id. Alerts must
// still reach it rather than failing against the id it was configured with.
func TestRecipientIsFollowedAcrossANewSessionID(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "s1")
	if err := os.WriteFile(filepath.Join(dir, "7.abc123.key"), []byte(`{"peerToken": "tok", "pidDomain": "darwin"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := newConnector(map[string]string{"session_id": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	// Pointed at the fixture registry rather than the real one.
	c.Resolve = trackedResolver(dir, "s1")

	if _, err := c.Resolve(); err != nil {
		t.Fatalf("resolve before /clear: %v", err)
	}
	writeRecord(t, dir, "s2")
	got, err := c.Resolve()
	if err != nil {
		t.Fatalf("resolve after /clear: %v", err)
	}
	if got.SocketPath != "/tmp/x.sock" || got.ProcStart != "P" {
		t.Errorf("recipient = %+v, want the same process", got)
	}
}

// The orchestrator runs /clear before the instance has sent it anything. The
// first alert must still land: the process is learned when the connector is
// built, not at the first delivery.
func TestFirstAlertAfterAnEarlyClearStillLands(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "s1")
	if err := os.WriteFile(filepath.Join(dir, "7.abc123.key"), []byte(`{"peerToken": "tok", "pidDomain": "darwin"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	resolve := trackedResolver(dir, "s1")
	writeRecord(t, dir, "s2")

	got, err := resolve()
	if err != nil {
		t.Fatalf("first resolve after /clear: %v", err)
	}
	if got.SocketPath != "/tmp/x.sock" || got.PeerToken != "tok" {
		t.Errorf("recipient = %+v, want the same process", got)
	}
}
