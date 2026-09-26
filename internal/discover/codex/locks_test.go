package codex

import (
	"os"
	"path/filepath"
	"testing"
)

// Liveness for a Codex thread is not "a record exists" — the thread store
// lists threads that ended long ago. A thread is live exactly when a running
// process holds its writer lock. See docs/providers.md#codex.

func writeLock(t *testing.T, dir, threadID string) string {
	t.Helper()
	path := filepath.Join(dir, threadID+".lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write lock fixture: %v", err)
	}
	return path
}

// heldBy returns a LockHolder reporting the given thread IDs as held.
func heldBy(held map[string]int) func(string) (int, bool) {
	return func(path string) (int, bool) {
		id := threadIDFromLockPath(path)
		pid, ok := held[id]
		return pid, ok
	}
}

func TestLiveThreadsReportsOnlyLocksAProcessHolds(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "01a0c3ae-d9a2-7950-9407-aa95d01ba913")
	writeLock(t, dir, "01a0c46d-1afb-7b52-80d5-f91870af433f")

	got, err := LiveThreads(LockParams{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"01a0c3ae-d9a2-7950-9407-aa95d01ba913": 4242}),
	})
	if err != nil {
		t.Fatalf("LiveThreads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d live threads, want 1", len(got))
	}
	holder, ok := got["01a0c3ae-d9a2-7950-9407-aa95d01ba913"]
	if !ok {
		t.Fatal("held thread missing from result")
	}
	if holder.PID != 4242 {
		t.Errorf("PID = %d, want 4242", holder.PID)
	}
}

// A lock file left behind by a crashed process is the common case, and it must
// not be read as a live thread.
func TestLiveThreadsIgnoresAbandonedLockFile(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "abandoned-thread")

	got, err := LiveThreads(LockParams{
		LockDir:    dir,
		LockHolder: heldBy(nil),
	})
	if err != nil {
		t.Fatalf("LiveThreads: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d live threads, want 0 — nobody holds the lock", len(got))
	}
}

func TestLiveThreadsIgnoresNonLockFiles(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "real-thread")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := LiveThreads(LockParams{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"real-thread": 1, "notes": 2, ".DS_Store": 3}),
	})
	if err != nil {
		t.Fatalf("LiveThreads: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d live threads, want 1", len(got))
	}
}

// No lock directory means Codex has never run here — zero threads, not an error.
func TestLiveThreadsReturnsEmptyWhenLockDirMissing(t *testing.T) {
	got, err := LiveThreads(LockParams{
		LockDir:    filepath.Join(t.TempDir(), "never-created"),
		LockHolder: heldBy(nil),
	})
	if err != nil {
		t.Fatalf("LiveThreads: %v, want no error for a missing lock dir", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d live threads, want 0", len(got))
	}
}

func TestThreadIDFromLockPathStripsDirAndSuffix(t *testing.T) {
	got := threadIDFromLockPath("/home/u/.codex/thread-writer-locks/01a0c3ae-dead-beef.lock")
	if want := "01a0c3ae-dead-beef"; got != want {
		t.Errorf("threadIDFromLockPath = %q, want %q", got, want)
	}
}

func TestDefaultLockDirIsUnderCodexHome(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("CODEX_HOME", "")
	got, err := DefaultLockDir()
	if err != nil {
		t.Fatalf("DefaultLockDir: %v", err)
	}
	if want := "/home/someone/.codex/thread-writer-locks"; got != want {
		t.Errorf("DefaultLockDir() = %q, want %q", got, want)
	}
}

// Codex honours CODEX_HOME, so a developer running against an alternate home
// must not silently be scanning the default one.
func TestDefaultLockDirHonoursCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/elsewhere/codex")
	got, err := DefaultLockDir()
	if err != nil {
		t.Fatalf("DefaultLockDir: %v", err)
	}
	if want := "/elsewhere/codex/thread-writer-locks"; got != want {
		t.Errorf("DefaultLockDir() = %q, want %q", got, want)
	}
}
