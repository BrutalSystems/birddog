package claudeinbox

import (
	"os"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
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
	c.Resolve = trackedResolver(live(claude.Params{RegistryDir: dir}), "s1")

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

	resolve := trackedResolver(live(claude.Params{RegistryDir: dir}), "s1")
	writeRecord(t, dir, "s2")

	got, err := resolve()
	if err != nil {
		t.Fatalf("first resolve after /clear: %v", err)
	}
	if got.SocketPath != "/tmp/x.sock" || got.PeerToken != "tok" {
		t.Errorf("recipient = %+v, want the same process", got)
	}
}

// A session's record may be reached through a link from another profile's
// registry. Its credential is beside the real record.
func TestTheCredentialIsReadBesideTheRealRecord(t *testing.T) {
	real, mirror := t.TempDir(), t.TempDir()
	writeRecord(t, real, "s1")
	if err := os.WriteFile(filepath.Join(real, "7.abc123.key"), []byte(`{"peerToken": "tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "7.json"), filepath.Join(mirror, "7.json")); err != nil {
		t.Fatal(err)
	}
	got, err := trackedResolver(live(claude.Params{Registries: func() []string { return []string{mirror} }}), "s1")()
	if err != nil || got.PeerToken != "tok" {
		t.Errorf("resolve = %+v, %v; want the token beside the real record", got, err)
	}
}

// live makes every fixture record answer, as a running session's would.
func live(p claude.Params) claude.Params {
	p.Probe = func(string) bool { return true }
	p.SameProcess = func(int, string) bool { return true }
	return p
}

// Liveness is real. A record at the orchestrator's process that does not
// answer is not followed to: it proves only that something once wrote it.
func TestADeadRecordIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "s1")
	if err := os.WriteFile(filepath.Join(dir, "7.abc123.key"), []byte(`{"peerToken": "tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	alive := true
	p := claude.Params{
		RegistryDir: dir,
		Probe:       func(string) bool { return alive },
		SameProcess: func(int, string) bool { return alive },
	}
	resolve := trackedResolver(p, "s1")

	alive = false
	writeRecord(t, dir, "s2")
	if _, err := resolve(); err == nil {
		t.Error("resolved to a record that does not answer")
	}
}

// The configured recipient is found whether or not it answers right now: a
// delivery failure is a better report than having nobody to deliver to.
func TestTheConfiguredRecipientIsFoundEvenWhenNotAnswering(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "s1")
	if err := os.WriteFile(filepath.Join(dir, "7.abc123.key"), []byte(`{"peerToken": "tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := claude.Params{RegistryDir: dir, Probe: func(string) bool { return false }}
	if got, err := trackedResolver(p, "s1")(); err != nil || got.PeerToken != "tok" {
		t.Errorf("resolve = %+v, %v; want the configured recipient", got, err)
	}
}
