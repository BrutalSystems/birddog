package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDirDefaultsToTheUserApplicationSupportDirectory(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("BIRDDOG_STATE_DIR", "")

	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if want := "/home/someone/Library/Application Support/birddog"; got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
}

func TestStateDirHonoursOverride(t *testing.T) {
	t.Setenv("BIRDDOG_STATE_DIR", "/tmp/elsewhere")
	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if got != "/tmp/elsewhere" {
		t.Errorf("StateDir() = %q, want the override", got)
	}
}

// Instance state is birddog's own, kept away from repositories and worktrees
// so it is never committed, scanned, or wiped with a clean checkout.
func TestInstanceStateLivesUnderTheStateDirectory(t *testing.T) {
	t.Setenv("BIRDDOG_STATE_DIR", "/tmp/bd-state")

	db, err := InstanceDB("inst-abc")
	if err != nil {
		t.Fatalf("InstanceDB: %v", err)
	}
	if !strings.HasPrefix(db, "/tmp/bd-state/") {
		t.Errorf("InstanceDB = %q, want it under the state directory", db)
	}
	if filepath.Ext(db) != ".db" {
		t.Errorf("InstanceDB = %q, want a .db file", db)
	}
}

func TestEnsureStateDirCreatesItOwnerOnly(t *testing.T) {
	base := filepath.Join(t.TempDir(), "state")
	t.Setenv("BIRDDOG_STATE_DIR", base)

	dir, err := EnsureStateDir()
	if err != nil {
		t.Fatalf("EnsureStateDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions = %04o, want 0700 — it records what the operator's sessions were doing", perm)
	}
}

// macOS caps sockaddr_un.sun_path at 104 bytes. The state directory alone
// (~/Library/Application Support/birddog/...) consumes most of that before an
// instance id is added, so the socket cannot live beside the database.
func TestSocketPathFitsWithinTheUnixSocketLimit(t *testing.T) {
	// A long home directory and the longest id callers may pass.
	t.Setenv("HOME", "/Users/a-user-with-a-fairly-long-account-name")
	t.Setenv("BIRDDOG_STATE_DIR", "")

	path, err := SocketPath(strings.Repeat("x", MaxInstanceIDLen))
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if len(path) > 104 {
		t.Errorf("socket path is %d bytes:\n%s\nmacOS refuses to bind past 104", len(path), path)
	}
}

func TestSocketPathIsDistinctPerInstance(t *testing.T) {
	a, err := SocketPath("inst-a")
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	b, err := SocketPath("inst-b")
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if a == b {
		t.Errorf("both instances got %q", a)
	}
}

func TestSocketPathRejectsAnOverlongInstanceID(t *testing.T) {
	_, err := SocketPath(strings.Repeat("x", MaxInstanceIDLen+1))
	if err == nil {
		t.Error("SocketPath = nil error for an overlong id, want it refused rather than silently truncated")
	}
}

// A path escaping the socket directory would let an id name any file on disk.
func TestSocketPathRejectsPathSeparatorsInTheID(t *testing.T) {
	for _, id := range []string{"../escape", "a/b", "."} {
		if _, err := SocketPath(id); err == nil {
			t.Errorf("SocketPath(%q) = nil error, want it refused", id)
		}
	}
}

// The lock is taken before the store opens, so nothing else has created the
// instances directory by then.
func TestEnsureInstanceDirCreatesItOwnerOnly(t *testing.T) {
	base := filepath.Join(t.TempDir(), "state")
	t.Setenv("BIRDDOG_STATE_DIR", base)

	dir, err := EnsureInstanceDir()
	if err != nil {
		t.Fatalf("EnsureInstanceDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions = %04o, want 0700", perm)
	}
}

func TestInstanceLockSitsBesideTheInstanceDatabase(t *testing.T) {
	t.Setenv("BIRDDOG_STATE_DIR", "/tmp/bd-state")

	db, err := InstanceDB("inst-abc")
	if err != nil {
		t.Fatalf("InstanceDB: %v", err)
	}
	lock, err := InstanceLock("inst-abc")
	if err != nil {
		t.Fatalf("InstanceLock: %v", err)
	}
	if filepath.Dir(db) != filepath.Dir(lock) {
		t.Errorf("db in %q but lock in %q", filepath.Dir(db), filepath.Dir(lock))
	}
}
