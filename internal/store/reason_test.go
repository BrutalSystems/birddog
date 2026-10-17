package store

import (
	"path/filepath"
	"testing"
	"time"
)

var reasonT0 = time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)

func reasonStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func observedAt(at time.Time, gen int64) Event {
	return Event{InstanceID: "i", TargetID: "w", Type: "observation", Source: "claude", At: at,
		SessionID: "s@P", Generation: gen, Status: "idle"}
}

func unreadableAt(at time.Time, gen int64) Event {
	return Event{InstanceID: "i", TargetID: "w", Type: "observation", Source: "claude", At: at,
		Generation: gen, Reason: "source_unreadable"}
}

// A target that has never been observed and fails from its first pass still
// has a reason to report, and no status is invented for it.
func TestAReasonIsRecordedForATargetNeverObserved(t *testing.T) {
	s := reasonStore(t)
	_, applied, err := s.Record(unreadableAt(reasonT0, 1))
	if err != nil || !applied {
		t.Fatalf("Record = applied %v, %v; want applied", applied, err)
	}
	st, ok, _ := s.TargetState("w")
	if !ok || st.Reason != "source_unreadable" || st.Status != "" || !st.ObservedAt.Equal(time.UnixMilli(0).UTC()) {
		t.Errorf("state = %+v, want only the reason", st)
	}

	// The first real observation is not overtaken by it.
	if _, applied, _ := s.Record(observedAt(reasonT0.Add(time.Second), 1)); !applied {
		t.Error("first observation after a reason-only row was not applied")
	}
	if st, _, _ := s.TargetState("w"); st.Status != "idle" || st.Reason != "" {
		t.Errorf("state = %+v, want the observation and no reason", st)
	}
}

// A delayed failure, older than a success already recorded, changes nothing
// and says so.
func TestADelayedReasonDoesNotOverwriteANewerObservation(t *testing.T) {
	s := reasonStore(t)
	s.Record(observedAt(reasonT0.Add(2*time.Second), 1))
	_, applied, err := s.Record(unreadableAt(reasonT0.Add(time.Second), 1))
	if err != nil || applied {
		t.Errorf("delayed reason: applied %v, %v; want not applied", applied, err)
	}
	if st, _, _ := s.TargetState("w"); st.Reason != "" || st.Status != "idle" {
		t.Errorf("state = %+v, want the newer observation intact", st)
	}

	// A straggler from a replaced run changes nothing either.
	s.Record(observedAt(reasonT0.Add(3*time.Second), 2))
	if _, applied, _ := s.Record(unreadableAt(reasonT0.Add(4*time.Second), 1)); applied {
		t.Error("a reason from a replaced run was applied")
	}
}

// A newer reason applies and keeps the last status as history; a delayed
// success between the two keeps the newer reason.
func TestReasonsAreOrderedAgainstObservations(t *testing.T) {
	s := reasonStore(t)
	s.Record(observedAt(reasonT0, 1))
	if _, applied, _ := s.Record(unreadableAt(reasonT0.Add(3*time.Second), 1)); !applied {
		t.Fatal("newer reason not applied")
	}
	if st, _, _ := s.TargetState("w"); st.Reason != "source_unreadable" || st.Status != "idle" || !st.ObservedAt.Equal(reasonT0) {
		t.Errorf("state = %+v, want the reason with the last status and its time kept", st)
	}

	s.Record(observedAt(reasonT0.Add(2*time.Second), 1)) // delayed, older than the reason
	if st, _, _ := s.TargetState("w"); st.Reason != "source_unreadable" {
		t.Errorf("reason = %q, want the newer reason kept against a delayed success", st.Reason)
	}

	s.Record(observedAt(reasonT0.Add(4*time.Second), 1)) // recovery
	if st, _, _ := s.TargetState("w"); st.Reason != "" {
		t.Errorf("reason = %q after recovery, want none", st.Reason)
	}
}
