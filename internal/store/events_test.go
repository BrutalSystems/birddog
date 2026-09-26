package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "birddog.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ev(target, typ string) Event {
	return Event{
		InstanceID: "inst-1",
		TargetID:   target,
		Type:       typ,
		Source:     "claude/registry",
		At:         time.UnixMilli(1789683614652).UTC(),
		Evidence:   json.RawMessage(`{"status":"idle"}`),
	}
}

func TestAppendAssignsIncreasingCursors(t *testing.T) {
	s := openTemp(t)

	first, err := s.Append(ev("worker-1", "idle"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	second, err := s.Append(ev("worker-1", "active"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if first < 1 {
		t.Errorf("first cursor = %d, want >= 1", first)
	}
	if second <= first {
		t.Errorf("cursors not increasing: %d then %d", first, second)
	}
}

func TestAfterZeroReturnsEverythingRetained(t *testing.T) {
	s := openTemp(t)
	for _, typ := range []string{"idle", "active", "exit"} {
		if _, err := s.Append(ev("worker-1", typ)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := s.After(0, 10)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0].Type != "idle" || got[2].Type != "exit" {
		t.Errorf("events out of order: %v", []string{got[0].Type, got[1].Type, got[2].Type})
	}
}

func TestAfterReturnsOnlyEventsPastTheCursor(t *testing.T) {
	s := openTemp(t)
	_, _ = s.Append(ev("worker-1", "idle"))
	cursor, _ := s.Append(ev("worker-1", "active"))
	_, _ = s.Append(ev("worker-1", "exit"))

	got, err := s.After(cursor, 10)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Type != "exit" {
		t.Errorf("Type = %q, want exit", got[0].Type)
	}
}

func TestAfterRespectsLimit(t *testing.T) {
	s := openTemp(t)
	for range 5 {
		_, _ = s.Append(ev("worker-1", "tick"))
	}

	got, err := s.After(0, 2)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d events, want 2", len(got))
	}
}

// Caught up is not an error: an orchestrator polling from the head expects an
// empty result, not a failure.
func TestAfterCurrentHeadReturnsEmpty(t *testing.T) {
	s := openTemp(t)
	head, _ := s.Append(ev("worker-1", "idle"))

	got, err := s.After(head, 10)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events, want 0", len(got))
	}
}

func TestEventRoundTripsAllFields(t *testing.T) {
	s := openTemp(t)
	in := Event{
		InstanceID: "inst-1",
		TargetID:   "worker-1",
		Type:       "input_requested",
		Source:     "claude/hook",
		At:         time.UnixMilli(1789683614652).UTC(),
		SessionID:  "9323ab40",
		Generation: 7,
		ConfigRev:  3,
		Evidence:   json.RawMessage(`{"tool":"Bash"}`),
	}
	if _, err := s.Append(in); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.After(0, 1)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	g := got[0]
	if g.InstanceID != in.InstanceID || g.TargetID != in.TargetID || g.Type != in.Type || g.Source != in.Source {
		t.Errorf("identity fields lost: %+v", g)
	}
	if !g.At.Equal(in.At) {
		t.Errorf("At = %v, want %v", g.At, in.At)
	}
	if g.SessionID != in.SessionID || g.Generation != in.Generation || g.ConfigRev != in.ConfigRev {
		t.Errorf("generation fields lost: %+v", g)
	}
	if string(g.Evidence) != string(in.Evidence) {
		t.Errorf("Evidence = %s, want %s", g.Evidence, in.Evidence)
	}
}

// Criterion 11: a cursor must survive birddog restarting, and replay must not
// silently lose events retained on disk.
func TestCursorReplayWorksAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "birddog.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, _ = s.Append(ev("worker-1", "idle"))
	cursor, _ := s.Append(ev("worker-1", "active"))
	_, _ = s.Append(ev("worker-1", "exit"))
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.After(cursor, 10)
	if err != nil {
		t.Fatalf("After across restart: %v", err)
	}
	if len(got) != 1 || got[0].Type != "exit" {
		t.Fatalf("got %v, want the single event after the cursor", got)
	}
}

// Cursors must keep climbing after a restart: reusing a sequence number would
// make an old cursor skip new events.
func TestCursorsKeepClimbingAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "birddog.db")

	s, _ := Open(path)
	last, _ := s.Append(ev("worker-1", "idle"))
	s.Close()

	reopened, _ := Open(path)
	defer reopened.Close()
	next, err := reopened.Append(ev("worker-1", "active"))
	if err != nil {
		t.Fatalf("Append after restart: %v", err)
	}
	if next <= last {
		t.Errorf("cursor went backwards across restart: %d then %d", last, next)
	}
}

// A cursor pointing into a gap must fail loudly. Silently returning later
// events would look like a successful read that simply had nothing in between.
func TestStaleCursorIsReportedNotSilentlySkipped(t *testing.T) {
	s := openTemp(t)
	for range 5 {
		_, _ = s.Append(ev("worker-1", "tick"))
	}
	if err := s.PruneBefore(4); err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}

	// Cursor 1 was valid once; events 2 and 3 are gone, so replay from it
	// cannot be honest.
	_, err := s.After(1, 10)
	if !errors.Is(err, ErrCursorStale) {
		t.Errorf("After(stale cursor) error = %v, want ErrCursorStale", err)
	}
}

func TestCursorAtTheRetentionBoundaryIsStillValid(t *testing.T) {
	s := openTemp(t)
	for range 5 {
		_, _ = s.Append(ev("worker-1", "tick"))
	}
	if err := s.PruneBefore(4); err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}

	// Everything after cursor 3 is retained, so this replay loses nothing.
	got, err := s.After(3, 10)
	if err != nil {
		t.Fatalf("After: %v, want no error at the retention boundary", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d events, want 2", len(got))
	}
}

func TestLatestCursorLetsAConsumerSnapshotThenFollow(t *testing.T) {
	s := openTemp(t)
	if c, err := s.LatestCursor(); err != nil || c != 0 {
		t.Fatalf("LatestCursor on empty store = %d, %v; want 0, nil", c, err)
	}
	last, _ := s.Append(ev("worker-1", "idle"))

	got, err := s.LatestCursor()
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if got != last {
		t.Errorf("LatestCursor = %d, want %d", got, last)
	}
}

// The observed status must survive the round trip: an event feed that drops it
// tells a reader what happened without saying what was seen.
func TestEventRetainsTheObservedStatus(t *testing.T) {
	s := openTemp(t)
	e := ev("worker-1", "status")
	e.Status = "active"
	if _, _, err := s.Record(e); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := s.After(0, 1)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if got[0].Status != "active" {
		t.Errorf("Status = %q, want it preserved in the event feed", got[0].Status)
	}
}

// A database created before the status column existed must keep working.
func TestStoreOpensADatabaseMissingTheStatusColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := openRaw(path, `CREATE TABLE events (
		seq INTEGER PRIMARY KEY AUTOINCREMENT, instance_id TEXT NOT NULL,
		target_id TEXT NOT NULL, type TEXT NOT NULL, source TEXT NOT NULL,
		at_ms INTEGER NOT NULL, session_id TEXT NOT NULL DEFAULT '',
		generation INTEGER NOT NULL DEFAULT 0, config_rev INTEGER NOT NULL DEFAULT 0,
		evidence BLOB);`)
	if err != nil {
		t.Fatalf("create legacy database: %v", err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-existing database: %v", err)
	}
	defer s.Close()

	e := ev("worker-1", "status")
	e.Status = "idle"
	if _, _, err := s.Record(e); err != nil {
		t.Fatalf("Record after migration: %v", err)
	}
	got, err := s.After(0, 1)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if got[0].Status != "idle" {
		t.Errorf("Status = %q, want the migrated column to hold it", got[0].Status)
	}
}

// Once retention has emptied the log, the cursor `status` reports must still be
// one `events` accepts. MAX(seq) is 0 on an empty table while the floor stands
// at the old maximum, so every replay from it would be refused as stale — and
// the documented recovery, taking a fresh cursor from `status`, would hand back
// the same rejected 0 forever. An instance whose targets have all exited never
// records again, which is precisely the state that empties the log.
func TestACursorFromAnEmptiedStoreIsStillFollowable(t *testing.T) {
	s := openTemp(t)
	for range 5 {
		_, _ = s.Append(ev("worker-1", "tick"))
	}
	// Past the newest sequence: the whole history goes.
	if err := s.PruneBefore(6); err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}

	cursor, err := s.LatestCursor()
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if cursor != 5 {
		t.Errorf("LatestCursor = %d, want 5: the floor is the honest follow-from point", cursor)
	}
	got, err := s.After(cursor, 10)
	if err != nil {
		t.Fatalf("After(%d): %v, want the cursor status reports to be followable", cursor, err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events from an emptied store, want 0", len(got))
	}
}

// The retention floor only ever rises. A later call with a lower seq says
// nothing about what an earlier one already deleted, and writing it would
// un-stale cursors that must still be refused. PruneBefore(1) is the case that
// reaches this in practice: it would write floor 0, which retentionFloor cannot
// tell apart from "never pruned".
func TestPruneBeforeNeverLowersTheRetentionFloor(t *testing.T) {
	s := openTemp(t)
	for range 5 {
		_, _ = s.Append(ev("worker-1", "tick"))
	}
	if err := s.PruneBefore(4); err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if err := s.PruneBefore(1); err != nil {
		t.Fatalf("PruneBefore(1): %v", err)
	}

	floor, err := s.retentionFloor()
	if err != nil {
		t.Fatalf("retentionFloor: %v", err)
	}
	if floor != 3 {
		t.Fatalf("retentionFloor = %d, want 3: the floor was reset by a lower prune", floor)
	}
	if _, err := s.After(1, 10); !errors.Is(err, ErrCursorStale) {
		t.Errorf("After(1) error = %v, want ErrCursorStale: events 2 and 3 are still gone", err)
	}
}
