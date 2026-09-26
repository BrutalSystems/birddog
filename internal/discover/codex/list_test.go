package codex

import (
	"testing"
)

type stubThreads []Thread

func (s stubThreads) list() ([]Thread, error) { return []Thread(s), nil }

func TestListJoinsLiveLocksWithThreadMetadata(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "live-thread")

	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"live-thread": 4242}),
		Threads: stubThreads{
			{ID: "live-thread", Name: "auth", CWD: "/work/api", Status: "idle", CanAcceptDirectInput: true},
		}.list,
		HolderCWD: func(int) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.ThreadID != "live-thread" || s.Name != "auth" || s.CWD != "/work/api" || s.Status != "idle" {
		t.Errorf("session = %+v", s)
	}
	if s.HolderPID != 4242 {
		t.Errorf("HolderPID = %d, want 4242", s.HolderPID)
	}
	if !s.Live {
		t.Error("Live = false, want true")
	}
}

// The state database lists threads that ended long ago. Appearing there is not
// evidence of anything; only a held writer lock is.
func TestListOmitsThreadsWithNoLiveLock(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "live-thread")

	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"live-thread": 1}),
		Threads: stubThreads{
			{ID: "live-thread", Name: "current"},
			{ID: "finished-last-week", Name: "history"},
			{ID: "also-finished", Name: "history"},
		}.list,
		HolderCWD: func(int) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1 — dead threads are history, not sessions", len(got))
	}
	if got[0].ThreadID != "live-thread" {
		t.Errorf("ThreadID = %q", got[0].ThreadID)
	}
}

// A thread that has not taken a turn yet holds a lock but has no state-DB
// metadata. It is still a live session, and the lock holder's working
// directory is the only thing that names it.
func TestListReportsLiveThreadWithNoMetadataUsingHolderCWD(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "brand-new-thread")

	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"brand-new-thread": 777}),
		Threads:    stubThreads{}.list,
		HolderCWD: func(pid int) (string, bool) {
			if pid == 777 {
				return "/work/fresh", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1 — a lock with no metadata is still live", len(got))
	}
	s := got[0]
	if s.CWD != "/work/fresh" {
		t.Errorf("CWD = %q, want the holder's working directory", s.CWD)
	}
	if s.Status != "" {
		t.Errorf("Status = %q, want empty — no metadata means no status to report", s.Status)
	}
	if !s.Live {
		t.Error("Live = false, want true")
	}
}

// Thread metadata must never override what the lock holder actually shows.
func TestListPrefersThreadMetadataCWDOverHolder(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "t1")

	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"t1": 5}),
		Threads:    stubThreads{{ID: "t1", CWD: "/from/metadata"}}.list,
		HolderCWD:  func(int) (string, bool) { return "/from/holder", true },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].CWD != "/from/metadata" {
		t.Errorf("CWD = %q, want the thread's own recorded cwd", got[0].CWD)
	}
}

// Losing the app-server must not hide sessions that the locks prove are live.
func TestListStillReportsLiveThreadsWhenMetadataIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "t1")

	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"t1": 9}),
		Threads:    func() ([]Thread, error) { return nil, errUnavailable },
		HolderCWD:  func(int) (string, bool) { return "/work/x", true },
	})
	if err != nil {
		t.Fatalf("List: %v, want liveness reported without metadata", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].MetadataAvailable {
		t.Error("MetadataAvailable = true, want false so the gap is visible rather than silent")
	}
}

func TestListSortsByThreadIDForStableOutput(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "zzz")
	writeLock(t, dir, "aaa")

	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"zzz": 1, "aaa": 2}),
		Threads:    stubThreads{}.list,
		HolderCWD:  func(int) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].ThreadID != "aaa" || got[1].ThreadID != "zzz" {
		t.Errorf("order = %q, %q; want aaa, zzz", got[0].ThreadID, got[1].ThreadID)
	}
}

// Reading metadata means spawning a codex app-server child. With no live
// locks there is nothing to describe, so the child must not be started.
func TestListDoesNotReadMetadataWhenNoThreadsAreLive(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "abandoned")

	called := false
	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(nil), // nobody holds it
		Threads: func() ([]Thread, error) {
			called = true
			return nil, nil
		},
		HolderCWD: func(int) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d sessions, want 0", len(got))
	}
	if called {
		t.Error("metadata was read with no live threads — that spawns a codex app-server for nothing")
	}
}

// Criterion 6: an adapter's own view must never be reported as the session's
// state. `notLoaded` means the thread is not loaded in *birddog's* app-server
// child — which is true of nearly every live thread, since each is owned by
// somebody else's process. It says nothing about what that session is doing.
func TestListDoesNotReportNotLoadedAsASessionState(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "t1")

	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"t1": 1}),
		Threads:    stubThreads{{ID: "t1", Name: "auth", Status: "notLoaded"}}.list,
		HolderCWD:  func(int) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	s := got[0]
	if s.StatusKnown {
		t.Error("StatusKnown = true for notLoaded — that is birddog's own view, not the session's")
	}
	if s.Status != "" {
		t.Errorf("Status = %q, want empty — the state is genuinely unknown", s.Status)
	}
	// The rest of the record is still good: the thread is live and named.
	if !s.Live || s.Name != "auth" {
		t.Errorf("session = %+v, want a live, named thread with an unknown state", s)
	}
}

func TestListReportsRealThreadStatesAsKnown(t *testing.T) {
	for _, status := range []string{"active", "idle", "systemError"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			writeLock(t, dir, "t1")
			got, err := List(Params{
				LockDir:    dir,
				LockHolder: heldBy(map[string]int{"t1": 1}),
				Threads:    stubThreads{{ID: "t1", Status: status}}.list,
				HolderCWD:  func(int) (string, bool) { return "", false },
			})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !got[0].StatusKnown {
				t.Errorf("StatusKnown = false for %q, want true", status)
			}
			if got[0].Status != status {
				t.Errorf("Status = %q, want %q", got[0].Status, status)
			}
		})
	}
}

// A thread with no metadata at all also has no known state — but for a
// different reason, and it must not be confused with an observed one.
func TestListReportsUnknownStatusWhenThreadHasNoMetadata(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "brand-new")

	got, err := List(Params{
		LockDir:    dir,
		LockHolder: heldBy(map[string]int{"brand-new": 1}),
		Threads:    stubThreads{}.list,
		HolderCWD:  func(int) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].StatusKnown {
		t.Error("StatusKnown = true with no metadata at all, want false")
	}
}
