package instance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func rec(id, name string) Record {
	return Record{
		ID: id, Name: name, PID: 4242,
		SocketPath: "/tmp/birddog-501/" + id + ".sock",
		ConfigPath: "/work/birddog.json",
		DBPath:     "/state/instances/" + id + ".db",
		StartedAt:  time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
	}
}

func TestRegisteredInstanceCanBeLoaded(t *testing.T) {
	dir := t.TempDir()
	if err := Register(dir, rec("inst-1", "checkout")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok, err := Load(dir, "inst-1")
	if err != nil || !ok {
		t.Fatalf("Load: %v ok=%v", err, ok)
	}
	if got.Name != "checkout" || got.PID != 4242 {
		t.Errorf("record = %+v", got)
	}
	if !got.StartedAt.Equal(rec("inst-1", "checkout").StartedAt) {
		t.Errorf("StartedAt = %v", got.StartedAt)
	}
}

func TestListReturnsEveryRegisteredInstance(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))
	_ = Register(dir, rec("inst-2", "billing"))

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d instances, want 2", len(got))
	}
	// Stable order, so output does not shuffle between calls.
	if got[0].ID != "inst-1" || got[1].ID != "inst-2" {
		t.Errorf("order = %q, %q", got[0].ID, got[1].ID)
	}
}

func TestListOnAnEmptyRegistryIsNotAnError(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("List: %v, want no error before any instance has run", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d instances, want 0", len(got))
	}
}

func TestDeregisterRemovesTheRecord(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))

	if err := Deregister(dir, "inst-1"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if _, ok, _ := Load(dir, "inst-1"); ok {
		t.Error("record still present after Deregister")
	}
}

// Stopping an instance that is already gone is the state the caller wanted.
func TestDeregisteringAnAbsentInstanceIsNotAnError(t *testing.T) {
	if err := Deregister(t.TempDir(), "never-existed"); err != nil {
		t.Errorf("Deregister: %v, want absence treated as success", err)
	}
}

// Records describe what the operator is watching; they are not for sharing.
func TestRecordsAreOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))

	info, err := os.Stat(filepath.Join(dir, "inst-1.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %04o, want 0600", perm)
	}
}

// Registration replaces cleanly, so a restarted instance does not leave the
// previous run's pid and socket behind.
func TestReregisteringReplacesThePreviousRecord(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))

	updated := rec("inst-1", "checkout")
	updated.PID = 9999
	if err := Register(dir, updated); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, _, _ := Load(dir, "inst-1")
	if got.PID != 9999 {
		t.Errorf("PID = %d, want the new run's", got.PID)
	}
	all, _ := List(dir)
	if len(all) != 1 {
		t.Errorf("got %d records, want 1", len(all))
	}
}

// A record left by a crashed daemon is stale, and listing must not pretend
// otherwise — whether an instance runs is decided by its lock, not its record.
func TestListMarksRecordsWhoseProcessIsGone(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))

	got, err := ListWithLiveness(dir, func(int) bool { return false })
	if err != nil {
		t.Fatalf("ListWithLiveness: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d instances, want 1", len(got))
	}
	if got[0].Running {
		t.Error("Running = true for an instance whose process is gone")
	}
}

func TestListReportsLiveInstancesAsRunning(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))

	got, _ := ListWithLiveness(dir, func(int) bool { return true })
	if !got[0].Running {
		t.Error("Running = false for a live instance")
	}
}

func TestRegisterRejectsAnUnusableID(t *testing.T) {
	for _, id := range []string{"", "../escape", "a/b"} {
		if err := Register(t.TempDir(), rec(id, "x")); err == nil {
			t.Errorf("Register(%q) = nil error, want it refused", id)
		}
	}
}

// Ids are minted here, so they are always usable as a filename and short
// enough for a socket path.
func TestNewIDIsUsableAsAPathComponent(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		id := NewID()
		if len(id) == 0 || len(id) > 24 {
			t.Fatalf("id %q is %d bytes, want 1..24", id, len(id))
		}
		for _, r := range id {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				t.Fatalf("id %q contains %q, want only lowercase, digits and hyphen", id, r)
			}
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
	}
}

// A stopped instance must remain discoverable, or it cannot be resumed and
// its state is unreachable except by knowing the id already.
func TestMarkStoppedKeepsTheRecord(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))

	stoppedAt := time.Date(2026, 9, 21, 13, 0, 0, 0, time.UTC)
	if err := MarkStopped(dir, "inst-1", stoppedAt); err != nil {
		t.Fatalf("MarkStopped: %v", err)
	}

	got, ok, err := Load(dir, "inst-1")
	if err != nil || !ok {
		t.Fatalf("Load after MarkStopped: %v ok=%v", err, ok)
	}
	if got.StoppedAt == nil {
		t.Fatal("StoppedAt not recorded")
	}
	if !got.StoppedAt.Equal(stoppedAt) {
		t.Errorf("StoppedAt = %v, want %v", got.StoppedAt, stoppedAt)
	}
	// Everything needed to resume it is still there.
	if got.ConfigPath == "" || got.DBPath == "" {
		t.Errorf("record lost what resuming needs: %+v", got)
	}
}

// A stopped instance is not running, whatever its pid says. That pid may since
// belong to something else entirely.
func TestStoppedInstanceIsNeverReportedRunning(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))
	_ = MarkStopped(dir, "inst-1", time.Now().UTC())

	got, err := ListWithLiveness(dir, func(int) bool { return true }) // pid reused
	if err != nil {
		t.Fatalf("ListWithLiveness: %v", err)
	}
	if got[0].Running {
		t.Error("Running = true for a stopped instance whose pid was reused")
	}
}

// Starting again clears the stop, so a resumed instance does not look stopped.
func TestRegisteringClearsAPreviousStop(t *testing.T) {
	dir := t.TempDir()
	_ = Register(dir, rec("inst-1", "checkout"))
	_ = MarkStopped(dir, "inst-1", time.Now().UTC())

	if err := Register(dir, rec("inst-1", "checkout")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, _, _ := Load(dir, "inst-1")
	if got.StoppedAt != nil {
		t.Error("StoppedAt survived a fresh registration")
	}
}

func TestMarkStoppedOnAnAbsentInstanceIsNotAnError(t *testing.T) {
	if err := MarkStopped(t.TempDir(), "never-existed", time.Now()); err != nil {
		t.Errorf("MarkStopped: %v, want absence treated as success", err)
	}
}
