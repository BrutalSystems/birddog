package store

import (
	"path/filepath"
	"testing"
)

// The identity is what makes a cursor mean something. It is minted once and
// must survive every reopen, or a restart would look like a replacement.
func TestStoreIdentitySurvivesReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := first.ID()
	if id == "" {
		t.Fatal("a new store has no identity")
	}
	_ = first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	if second.ID() != id {
		t.Errorf("identity changed across a reopen: %q then %q", id, second.ID())
	}
}

// A replaced store is a different history. If two stores could share an
// identity, a cursor from one would be silently honoured by the other — which
// is the whole failure this exists to prevent.
func TestASeparateStoreGetsASeparateIdentity(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	defer a.Close()
	b, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	defer b.Close()

	if a.ID() == b.ID() {
		t.Errorf("two stores share the identity %q", a.ID())
	}
}

// The case this is for. A consumer holds a cursor from a store that has since
// been replaced — birddog reinstalled, its state directory cleared, the
// instance recreated. Sequences restart at 1, so the old cursor is above
// everything and the retention floor is absent, which means the staleness
// check passes and the query matches nothing.
//
// Without an identity the answer is an empty page: there is nothing new. That
// is the one thing birddog must never manufacture, because to a consumer it is
// indistinguishable from a session that has gone quiet.
func TestACursorFromAReplacedStoreIsNotReportedAsSilence(t *testing.T) {
	dir := t.TempDir()
	old, err := Open(filepath.Join(dir, "old.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 20; i++ {
		if _, err := old.Append(ev("worker-1", "tick")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	carried, err := old.LatestCursor()
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	oldID := old.ID()
	_ = old.Close()

	// The store is gone and a fresh one stands in its place.
	fresh, err := Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	defer fresh.Close()

	// Today's answer, and why it is not good enough on its own.
	events, err := fresh.After(carried, 100)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected the empty page that makes this dangerous, got %d events", len(events))
	}

	// The identity is what turns that silence into an answer.
	if fresh.ID() == oldID {
		t.Fatal("the fresh store reused the replaced store's identity")
	}
}
