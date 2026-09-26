package store

import (
	"path/filepath"
	"testing"
	"time"
)

func obs(target, status string, gen int64, at time.Time) Event {
	return Event{
		InstanceID: "inst-1",
		TargetID:   target,
		Type:       "status",
		Source:     "claude/registry",
		At:         at,
		SessionID:  "session-a",
		Generation: gen,
		Status:     status,
	}
}

var t0 = time.UnixMilli(1789683000000).UTC()

func TestRecordMakesAnObservationTheTargetsCurrentState(t *testing.T) {
	s := openTemp(t)

	seq, applied, err := s.Record(obs("worker-1", "idle", 1, t0))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if seq < 1 {
		t.Errorf("seq = %d, want >= 1", seq)
	}
	if !applied {
		t.Fatal("applied = false, want true for the first observation")
	}

	got, ok, err := s.TargetState("worker-1")
	if err != nil {
		t.Fatalf("TargetState: %v", err)
	}
	if !ok {
		t.Fatal("no state recorded")
	}
	if got.Status != "idle" || got.Generation != 1 {
		t.Errorf("state = %+v", got)
	}
}

// Criterion 16: an observation from a session generation that has been
// replaced must not become the replacement's state.
func TestLateEventFromAnOldGenerationCannotUpdateState(t *testing.T) {
	s := openTemp(t)

	if _, _, err := s.Record(obs("worker-1", "idle", 1, t0)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// The session is replaced; the new one reports being active.
	if _, _, err := s.Record(obs("worker-1", "active", 2, t0.Add(time.Second))); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// A straggler from the old session arrives late, claiming it exited.
	_, applied, err := s.Record(obs("worker-1", "exited", 1, t0.Add(2*time.Second)))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if applied {
		t.Error("applied = true for an old generation, want false")
	}

	got, _, err := s.TargetState("worker-1")
	if err != nil {
		t.Fatalf("TargetState: %v", err)
	}
	if got.Status != "active" || got.Generation != 2 {
		t.Errorf("state = %+v, want the replacement session's state untouched", got)
	}
}

// The rejected event is still history. Refusing to let it change state is not
// a reason to lose the record that it arrived.
func TestRejectedLateEventIsStillRecordedInTheLog(t *testing.T) {
	s := openTemp(t)
	_, _, _ = s.Record(obs("worker-1", "active", 2, t0))

	seq, applied, err := s.Record(obs("worker-1", "exited", 1, t0.Add(time.Second)))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if applied {
		t.Fatal("applied = true, want false")
	}
	if seq < 1 {
		t.Fatal("rejected event was not appended")
	}

	events, err := s.After(0, 10)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("got %d events, want 2 — the rejected observation is still history", len(events))
	}
}

// Within one generation, observations can still arrive out of order.
func TestOlderObservationInTheSameGenerationIsRejected(t *testing.T) {
	s := openTemp(t)
	_, _, _ = s.Record(obs("worker-1", "active", 1, t0.Add(time.Minute)))

	_, applied, err := s.Record(obs("worker-1", "idle", 1, t0))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if applied {
		t.Error("applied = true for an older observation in the same generation, want false")
	}
	got, _, _ := s.TargetState("worker-1")
	if got.Status != "active" {
		t.Errorf("Status = %q, want the newer observation retained", got.Status)
	}
}

func TestNewerObservationInTheSameGenerationIsApplied(t *testing.T) {
	s := openTemp(t)
	_, _, _ = s.Record(obs("worker-1", "idle", 1, t0))

	_, applied, err := s.Record(obs("worker-1", "active", 1, t0.Add(time.Minute)))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !applied {
		t.Error("applied = false, want true for a newer observation")
	}
	got, _, _ := s.TargetState("worker-1")
	if got.Status != "active" {
		t.Errorf("Status = %q, want active", got.Status)
	}
}

// Targets are independent: one session's generation says nothing about another.
func TestGenerationGuardIsPerTarget(t *testing.T) {
	s := openTemp(t)
	_, _, _ = s.Record(obs("worker-1", "active", 5, t0))

	_, applied, err := s.Record(obs("worker-2", "idle", 1, t0))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !applied {
		t.Error("applied = false — another target's generation must not gate this one")
	}
}

// An event that asserts no status is a record, not an observation of state.
func TestEventWithNoStatusDoesNotTouchState(t *testing.T) {
	s := openTemp(t)
	_, _, _ = s.Record(obs("worker-1", "active", 1, t0))

	e := obs("worker-1", "", 2, t0.Add(time.Minute))
	e.Type = "notification_delivered"
	_, applied, err := s.Record(e)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if applied {
		t.Error("applied = true for an event asserting no status, want false")
	}
	got, _, _ := s.TargetState("worker-1")
	if got.Status != "active" || got.Generation != 1 {
		t.Errorf("state = %+v, want unchanged", got)
	}
}

func TestTargetStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "birddog.db")

	s, _ := Open(path)
	_, _, _ = s.Record(obs("worker-1", "active", 3, t0))
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, ok, err := reopened.TargetState("worker-1")
	if err != nil || !ok {
		t.Fatalf("TargetState after restart: %v, ok=%v", err, ok)
	}
	if got.Status != "active" || got.Generation != 3 {
		t.Errorf("state = %+v, want it preserved across restart", got)
	}
}

func TestTargetStateReportsAbsenceForUnknownTarget(t *testing.T) {
	s := openTemp(t)
	if _, ok, err := s.TargetState("never-seen"); err != nil || ok {
		t.Errorf("TargetState(unknown) = ok %v, err %v; want false, nil", ok, err)
	}
}

// Visibility travels with the observation. Without it, status would have to
// hardcode "unavailable" for every provider, and the one provider that can
// actually see a permission request could never say so.
func TestTargetStateCarriesInputRequestVisibility(t *testing.T) {
	s := openTemp(t)
	e := obs("worker-1", "waiting_input", 1, t0)
	e.InputRequestVisibility = "observed"
	if _, _, err := s.Record(e); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, ok, err := s.TargetState("worker-1")
	if err != nil || !ok {
		t.Fatalf("TargetState: %v ok=%v", err, ok)
	}
	if got.InputRequestVisibility != "observed" {
		t.Errorf("InputRequestVisibility = %q, want observed", got.InputRequestVisibility)
	}
}

// An observation that asserts no visibility must not erase what was last
// known about it.
func TestVisibilityDefaultsToUnavailableRatherThanEmpty(t *testing.T) {
	s := openTemp(t)
	if _, _, err := s.Record(obs("worker-1", "active", 1, t0)); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, _, _ := s.TargetState("worker-1")
	if got.InputRequestVisibility != "unavailable" {
		t.Errorf("InputRequestVisibility = %q, want unavailable — never an empty claim", got.InputRequestVisibility)
	}
}

// The reason travels with the observation into the durable record, so a
// consumer reading state later learns why an answer was indeterminate rather
// than only that it was.
func TestReasonRoundTripsIntoTargetState(t *testing.T) {
	s := openTemp(t)
	e := ev("worker-1", "observation")
	e.Status = "unknown"
	e.Reason = "provider_limited"

	if _, _, err := s.Record(e); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, ok, err := s.TargetState("worker-1")
	if err != nil || !ok {
		t.Fatalf("TargetState: %v ok=%v", err, ok)
	}
	if got.Reason != "provider_limited" {
		t.Errorf("Reason = %q, want it preserved", got.Reason)
	}
}

// And onto the event feed, so a reader following from a cursor sees it too.
func TestReasonRoundTripsOntoTheEventFeed(t *testing.T) {
	s := openTemp(t)
	e := ev("worker-1", "observation")
	e.Reason = "contact_lost"

	if _, err := s.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	events, err := s.After(0, 10)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if events[0].Reason != "contact_lost" {
		t.Errorf("Reason = %q, want it carried on the event", events[0].Reason)
	}
}

// A database written before reasons existed must keep working, with no reason
// rather than a wrong one.
func TestAStoreWithoutTheReasonColumnStillOpens(t *testing.T) {
	s := openTemp(t)
	if _, _, err := s.Record(ev("worker-1", "observation")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, _, err := s.TargetState("worker-1")
	if err != nil {
		t.Fatalf("TargetState: %v", err)
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty when none was recorded", got.Reason)
	}
}
