package codex

import (
	"os"
	"path/filepath"
	"testing"
)

// LsofHolder is the real liveness primitive behind LiveThreads, so it is
// exercised against files this process genuinely holds open.

func TestLsofHolderReportsThisProcessHoldingAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.lock")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	pid, held := LsofHolder(path)
	if !held {
		t.Fatal("held = false, want true for a file this process has open")
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want this process %d", pid, os.Getpid())
	}
}

func TestLsofHolderReportsNotHeldAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "released.lock")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	if _, held := LsofHolder(path); held {
		t.Error("held = true, want false once the file is closed")
	}
}

// lsof exits nonzero when nothing holds the file. That is the ordinary "not
// held" answer, not a failure to be reported as an error.
func TestLsofHolderReportsNotHeldForMissingFile(t *testing.T) {
	if _, held := LsofHolder(filepath.Join(t.TempDir(), "never-existed.lock")); held {
		t.Error("held = true, want false for a file that does not exist")
	}
}

func TestLsofHolderHandlesPathsContainingSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a directory with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "a lock with spaces.lock")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	pid, held := LsofHolder(path)
	if !held {
		t.Fatal("held = false, want true — a path with spaces must not break the lookup")
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}
}

// A Codex thread that has not taken a turn has no state-DB record, so the
// holder's working directory is the only thing that names it.
func TestHolderCWDReportsThisProcessWorkingDirectory(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	got, ok := HolderCWD(os.Getpid())
	if !ok {
		t.Fatal("ok = false, want true for this process")
	}
	if got != want {
		t.Errorf("HolderCWD = %q, want %q", got, want)
	}
}

func TestHolderCWDReportsNotFoundForUnusedPID(t *testing.T) {
	if _, ok := HolderCWD(4194303); ok {
		t.Error("ok = true, want false for a pid nothing is running under")
	}
}
