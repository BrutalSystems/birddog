package lockfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestAcquireTakesAFreeLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file not created: %v", err)
	}
}

// The whole point: a second daemon for the same instance would write the same
// database and duplicate every alert.
func TestAcquireRefusesALockAlreadyHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer first.Release()

	second, err := Acquire(path)
	if err == nil {
		second.Release()
		t.Fatal("Acquire = nil error while the lock is held, want it refused")
	}
	if !IsHeld(err) {
		t.Errorf("error = %v, want it recognisable as already-held", err)
	}
}

func TestReleasedLockCanBeTakenAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	second.Release()
}

// A daemon that died leaves its lock file behind. The file is not the lock —
// the holder is — so a stale file must not keep an instance unstartable.
func TestStaleLockFileFromADeadProcessIsReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	// A process that exited while holding the lock leaves the file in place.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldsLockThenExits")
	cmd.Env = append(os.Environ(), "BIRDDOG_LOCK_HELPER="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("helper did not leave the lock file: %v", err)
	}

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire over a dead holder's file: %v — a stale file must not block startup", err)
	}
	l.Release()
}

// Run only as the subprocess above: takes the lock and exits without releasing.
func TestHelperHoldsLockThenExits(t *testing.T) {
	path := os.Getenv("BIRDDOG_LOCK_HELPER")
	if path == "" {
		t.Skip("helper process only")
	}
	if _, err := Acquire(path); err != nil {
		t.Fatalf("helper Acquire: %v", err)
	}
	// Exit without releasing, as a crashed daemon would.
}

// The holder's pid is recorded so an operator told "already running" can find
// out what is holding it.
func TestLockRecordsTheHoldingProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatalf("lock file does not contain a pid: %q", data)
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}
}

func TestHolderPIDReportsWhoHasIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	pid, ok := HolderPID(path)
	if !ok {
		t.Fatal("HolderPID = not found, want the holder")
	}
	if pid != os.Getpid() {
		t.Errorf("HolderPID = %d, want %d", pid, os.Getpid())
	}
}
