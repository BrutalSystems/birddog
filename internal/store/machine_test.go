package store

import (
	"path/filepath"
	"testing"
	"time"
)

func machineStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// The machine an observation was made on travels with the observation, so it
// is still true after the event leaves the machine that recorded it.
func TestEventCarriesItsMachineThroughAStoreRoundTrip(t *testing.T) {
	st := machineStore(t)
	if _, _, err := st.Record(Event{
		InstanceID: "i1", TargetID: "t1", Type: "observation", Source: "claude",
		At: time.Now(), Status: "active", Machine: "ferry:7c3a91b2",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	events, err := st.After(0, 10)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Machine != "ferry:7c3a91b2" {
		t.Errorf("machine = %q, want ferry:7c3a91b2", events[0].Machine)
	}
}

// Local mode records no machine, and reads back none.
func TestEventWithoutAMachineReadsBackEmpty(t *testing.T) {
	st := machineStore(t)
	if _, _, err := st.Record(Event{
		InstanceID: "i1", TargetID: "t1", Type: "observation", Source: "claude",
		At: time.Now(), Status: "active",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	events, _ := st.After(0, 10)
	if events[0].Machine != "" {
		t.Errorf("machine = %q, want empty", events[0].Machine)
	}
}

// A row written before the column existed must read back as empty rather than
// inheriting the machine this daemon happens to be configured as. Back-filling
// would make history claim an origin it was never observed under.
func TestMigrationLeavesOlderRowsUnattributed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// The events table as it stood before this column, with one row in it.
	const oldSchema = `
CREATE TABLE IF NOT EXISTS events (
	seq         INTEGER PRIMARY KEY AUTOINCREMENT,
	instance_id TEXT    NOT NULL,
	target_id   TEXT    NOT NULL,
	type        TEXT    NOT NULL,
	source      TEXT    NOT NULL,
	at_ms       INTEGER NOT NULL,
	session_id  TEXT    NOT NULL DEFAULT '',
	generation  INTEGER NOT NULL DEFAULT 0,
	config_rev  INTEGER NOT NULL DEFAULT 0,
	evidence    BLOB
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`
	db, err := openRaw(path, oldSchema)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO events (instance_id, target_id, type, source, at_ms) VALUES ('i1','t1','observation','claude', ?)`,
		time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()

	events, err := st.After(0, 10)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want the pre-existing row preserved", len(events))
	}
	if events[0].Machine != "" {
		t.Errorf("machine = %q, want empty: a row observed before the column existed has no machine", events[0].Machine)
	}
}

// Target state is durable and outlives the process that wrote it, so the
// machine an observation was made on has to be stored beside it. Reporting the
// reading daemon's current identity instead would qualify a stored session
// reference with a machine it was never observed under — the same mistake the
// per-event column exists to prevent, surviving on the state half.
func TestTargetStateRemembersTheMachineItWasObservedUnder(t *testing.T) {
	st := machineStore(t)
	if _, applied, err := st.Record(Event{
		InstanceID: "i1", TargetID: "t1", Type: "observation", Source: "fake",
		At: time.Now(), Status: "active", SessionID: "ses_1", Machine: "A",
	}); err != nil || !applied {
		t.Fatalf("record: applied=%v err=%v", applied, err)
	}

	rec, ok, err := st.TargetState("t1")
	if err != nil || !ok {
		t.Fatalf("target state: ok=%v err=%v", ok, err)
	}
	if rec.Machine != "A" {
		t.Errorf("machine = %q, want A — the machine the session was observed on", rec.Machine)
	}
}
