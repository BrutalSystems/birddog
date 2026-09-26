package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/policy"
)

var r0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func retentionStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// observation appends an ordinary observation at a time.
func observation(t *testing.T, s *Store, target string, gen int64, at time.Time) int64 {
	t.Helper()
	seq, _, err := s.Record(Event{
		InstanceID: "i", TargetID: target, Type: "observation", Source: "fake",
		At: at, SessionID: "sess", Generation: gen, Status: "idle",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return seq
}

// exitEvent appends a verified exit at a time.
func exitEvent(t *testing.T, s *Store, target string, gen int64, at time.Time) int64 {
	t.Helper()
	seq, _, err := s.Record(Event{
		InstanceID: "i", TargetID: target, Type: "observation", Source: "fake",
		At: at, SessionID: "sess", Generation: gen, Status: statusExited,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return seq
}

func TestRetainFromOnAnEmptyStoreIsANoOp(t *testing.T) {
	s := retentionStore(t)

	from, err := s.RetainFrom(r0, time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != 1 {
		t.Errorf("RetainFrom = %d, want 1: nothing recorded means nothing to drop", from)
	}
	if err := s.PruneBefore(from); err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
}

func TestRetainFromKeepsEventsInsideTheWindow(t *testing.T) {
	s := retentionStore(t)
	observation(t, s, "w1", 1, r0.Add(-3*time.Hour))
	observation(t, s, "w1", 1, r0.Add(-2*time.Hour))
	keep := observation(t, s, "w1", 1, r0.Add(-30*time.Minute))

	from, err := s.RetainFrom(r0.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != keep {
		t.Errorf("RetainFrom = %d, want %d: the two older events are outside the window", from, keep)
	}
}

func TestAnEventExactlyAtTheCutoffIsRetained(t *testing.T) {
	s := retentionStore(t)
	observation(t, s, "w1", 1, r0.Add(-2*time.Hour))
	boundary := observation(t, s, "w1", 1, r0.Add(-time.Hour))

	from, err := s.RetainFrom(r0.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != boundary {
		t.Errorf("RetainFrom = %d, want %d: the boundary belongs to the retained history", from, boundary)
	}
}

func TestAnUnacknowledgedExitIsHeldPastTheWindow(t *testing.T) {
	s := retentionStore(t)
	gone := exitEvent(t, s, "w1", 1, r0.Add(-2*time.Hour))
	if _, _, err := s.OpenOrTouchIncident("w1", conditionExit, 1, r0.Add(-2*time.Hour)); err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	observation(t, s, "w1", 1, r0.Add(-30*time.Minute))

	// Outside the one-hour window, but inside the four-hour hold.
	from, err := s.RetainFrom(r0.Add(-time.Hour), 4*time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != gone {
		t.Errorf("RetainFrom = %d, want %d: an uncollected terminal outcome is held", from, gone)
	}
}

func TestAnAcknowledgedExitIsPrunedNormally(t *testing.T) {
	s := retentionStore(t)
	exitEvent(t, s, "w1", 1, r0.Add(-2*time.Hour))
	inc, _, err := s.OpenOrTouchIncident("w1", conditionExit, 1, r0.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	if err := s.AcknowledgeIncident(inc.ID, r0.Add(-90*time.Minute)); err != nil {
		t.Fatalf("AcknowledgeIncident: %v", err)
	}
	keep := observation(t, s, "w1", 1, r0.Add(-30*time.Minute))

	from, err := s.RetainFrom(r0.Add(-time.Hour), 4*time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != keep {
		t.Errorf("RetainFrom = %d, want %d: acknowledged is collected", from, keep)
	}
}

func TestAnExitIsDroppedOnceTheHoldElapses(t *testing.T) {
	s := retentionStore(t)
	exitEvent(t, s, "w1", 1, r0.Add(-10*time.Hour))
	if _, _, err := s.OpenOrTouchIncident("w1", conditionExit, 1, r0.Add(-10*time.Hour)); err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	keep := observation(t, s, "w1", 1, r0.Add(-30*time.Minute))

	// Window one hour, hold two more: the exit is nine hours past both.
	from, err := s.RetainFrom(r0.Add(-time.Hour), 2*time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != keep {
		t.Errorf("RetainFrom = %d, want %d: the hold is a bound, not an exemption", from, keep)
	}
}

func TestAnExitWithNoIncidentIsHeld(t *testing.T) {
	s := retentionStore(t)
	gone := exitEvent(t, s, "w1", 1, r0.Add(-2*time.Hour))
	observation(t, s, "w1", 1, r0.Add(-30*time.Minute))

	// No incident at all: a policy that does not alert on exit has nothing to
	// acknowledge, which is uncollected rather than collected.
	from, err := s.RetainFrom(r0.Add(-time.Hour), 4*time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != gone {
		t.Errorf("RetainFrom = %d, want %d: no incident is not consent", from, gone)
	}
}

func TestAResolvedButUnacknowledgedExitIsStillHeld(t *testing.T) {
	s := retentionStore(t)
	gone := exitEvent(t, s, "w1", 1, r0.Add(-2*time.Hour))
	inc, _, err := s.OpenOrTouchIncident("w1", conditionExit, 1, r0.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	if err := s.ResolveIncident(inc.ID, r0.Add(-100*time.Minute)); err != nil {
		t.Fatalf("ResolveIncident: %v", err)
	}
	observation(t, s, "w1", 1, r0.Add(-30*time.Minute))

	from, err := s.RetainFrom(r0.Add(-time.Hour), 4*time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != gone {
		t.Errorf("RetainFrom = %d, want %d: resolved is not collected", from, gone)
	}
}

func TestAHeldOutcomeHoldsEverythingAfterIt(t *testing.T) {
	s := retentionStore(t)
	gone := exitEvent(t, s, "w1", 1, r0.Add(-5*time.Hour))
	if _, _, err := s.OpenOrTouchIncident("w1", conditionExit, 1, r0.Add(-5*time.Hour)); err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	// Unrelated, and also outside the window: retained anyway, because the
	// floor is one contiguous boundary.
	later := observation(t, s, "w2", 1, r0.Add(-4*time.Hour))

	from, err := s.RetainFrom(r0.Add(-time.Hour), 10*time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != gone {
		t.Fatalf("RetainFrom = %d, want %d", from, gone)
	}
	if err := s.PruneBefore(from); err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	events, err := s.After(gone-1, 100)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) != 2 || events[0].Seq != gone || events[1].Seq != later {
		t.Errorf("retained %+v, want the held exit and the later event", events)
	}
}

// TestOneUnacknowledgedIncidentAmongSeveralStillHoldsTheExit pins that the
// held query is per-incident-row, not per-target: the unique index on
// incidents only forbids two *unresolved* rows for the same target,
// condition and generation, so a resolved-and-acknowledged incident and a
// later, still-open, unacknowledged one can coexist. Any one unacknowledged
// match must hold the event — a rewrite to EXISTS or a grouped aggregate
// that lost this would flip it to the permissive direction silently.
func TestOneUnacknowledgedIncidentAmongSeveralStillHoldsTheExit(t *testing.T) {
	s := retentionStore(t)
	gone := exitEvent(t, s, "w1", 1, r0.Add(-2*time.Hour))

	first, _, err := s.OpenOrTouchIncident("w1", conditionExit, 1, r0.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("OpenOrTouchIncident (first): %v", err)
	}
	if err := s.AcknowledgeIncident(first.ID, r0.Add(-110*time.Minute)); err != nil {
		t.Fatalf("AcknowledgeIncident: %v", err)
	}
	if err := s.ResolveIncident(first.ID, r0.Add(-100*time.Minute)); err != nil {
		t.Fatalf("ResolveIncident: %v", err)
	}

	// The first incident is resolved, so this opens a second, distinct
	// incident for the same target, condition and generation. It is never
	// acknowledged.
	if _, opened, err := s.OpenOrTouchIncident("w1", conditionExit, 1, r0.Add(-95*time.Minute)); err != nil {
		t.Fatalf("OpenOrTouchIncident (second): %v", err)
	} else if !opened {
		t.Fatalf("second OpenOrTouchIncident did not open a new incident")
	}

	observation(t, s, "w1", 1, r0.Add(-30*time.Minute))

	from, err := s.RetainFrom(r0.Add(-time.Hour), 4*time.Hour)
	if err != nil {
		t.Fatalf("RetainFrom: %v", err)
	}
	if from != gone {
		t.Errorf("RetainFrom = %d, want %d: the second, unacknowledged incident still holds the exit", from, gone)
	}
}

// TestTheMirroredExitConstantsHaveNotDrifted guards the two values retention.go
// has to duplicate, because the store cannot import the packages that define
// them — the dependency runs the other way.
//
// It exists because drift here is silent and unrecoverable. A rename of either
// original leaves the held query matching nothing, so the hold stops existing
// and retention prunes the uncollected terminal outcome the whole feature is
// built to protect. Nothing else in the tree would fail.
//
// A test file may import both packages freely: internal/policy depends only on
// internal/config, so there is no cycle and no production dependency.
func TestTheMirroredExitConstantsHaveNotDrifted(t *testing.T) {
	if statusExited != policy.StatusExited {
		t.Errorf("statusExited = %q, but policy.StatusExited = %q: the hold no longer matches an exit",
			statusExited, policy.StatusExited)
	}
	if conditionExit != config.ConditionExit {
		t.Errorf("conditionExit = %q, but config.ConditionExit = %q: the hold no longer finds its incident",
			conditionExit, config.ConditionExit)
	}
}
