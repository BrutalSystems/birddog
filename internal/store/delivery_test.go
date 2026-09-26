package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openIncident(t *testing.T, s *Store) int64 {
	t.Helper()
	inc, _, err := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)
	if err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	return inc.ID
}

func TestAnUnnotifiedIncidentShouldBeDelivered(t *testing.T) {
	s := openTemp(t)
	id := openIncident(t, s)

	should, err := s.ShouldAttemptDelivery(id, 3)
	if err != nil {
		t.Fatalf("ShouldAttemptDelivery: %v", err)
	}
	if !should {
		t.Error("should = false for an incident nobody has been told about")
	}
}

func TestADeliveredIncidentIsNotSentAgain(t *testing.T) {
	s := openTemp(t)
	id := openIncident(t, s)
	if err := s.RecordDelivery(id, "fake", "delivered", t0); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}

	should, _ := s.ShouldAttemptDelivery(id, 3)
	if should {
		t.Error("should = true for an incident already delivered — that re-notifies an unchanged condition")
	}
}

// Criterion 18: an uncertain delivery must not be blindly resent. Nothing
// established that it failed, and a duplicate may be worse than a silence.
func TestAnUncertainDeliveryIsNotResent(t *testing.T) {
	s := openTemp(t)
	id := openIncident(t, s)
	if err := s.RecordDelivery(id, "fake", "delivery_unknown", t0); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}

	should, _ := s.ShouldAttemptDelivery(id, 3)
	if should {
		t.Error("should = true after an uncertain delivery — a blind resend risks a duplicate nobody can deduplicate")
	}
}

// A definite failure is different: nothing arrived, so retrying is safe.
func TestADefiniteFailureIsRetriedWithinItsBound(t *testing.T) {
	s := openTemp(t)
	id := openIncident(t, s)

	for i := range 2 {
		if err := s.RecordDelivery(id, "fake", "failed", t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("RecordDelivery: %v", err)
		}
		should, _ := s.ShouldAttemptDelivery(id, 3)
		if !should {
			t.Fatalf("should = false after %d definite failures, want a retry within the bound", i+1)
		}
	}
}

// Retries are bounded: an orchestrator that is gone must not be hammered.
func TestRetriesStopAtTheBound(t *testing.T) {
	s := openTemp(t)
	id := openIncident(t, s)

	for range 3 {
		_ = s.RecordDelivery(id, "fake", "failed", t0)
	}
	should, _ := s.ShouldAttemptDelivery(id, 3)
	if should {
		t.Error("should = true past the retry bound")
	}
}

// Criterion 18: the state survives a restart, or every incident is re-sent the
// moment birddog comes back.
func TestDeliveryStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.db")

	s, _ := Open(path)
	id := openIncident(t, s)
	_ = s.RecordDelivery(id, "fake", "delivered", t0)
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	should, err := reopened.ShouldAttemptDelivery(id, 3)
	if err != nil {
		t.Fatalf("ShouldAttemptDelivery: %v", err)
	}
	if should {
		t.Error("should = true after restart for an already delivered incident")
	}
}

func TestDeliveryStatusReportsWhatHappened(t *testing.T) {
	s := openTemp(t)
	id := openIncident(t, s)
	_ = s.RecordDelivery(id, "inbox", "delivery_unknown", t0)

	got, ok, err := s.DeliveryStatus(id)
	if err != nil || !ok {
		t.Fatalf("DeliveryStatus: %v ok=%v", err, ok)
	}
	if got.Outcome != "delivery_unknown" || got.Transport != "inbox" {
		t.Errorf("status = %+v", got)
	}
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}
}

// Criterion 18: an uncertain delivery must not cause a switch to another
// transport, which would risk the same alert arriving twice by two routes.
func TestUncertainDeliveryIsNotRetriedThroughAnotherTransport(t *testing.T) {
	s := openTemp(t)
	id := openIncident(t, s)
	_ = s.RecordDelivery(id, "inbox", "delivery_unknown", t0)

	should, _ := s.ShouldAttemptDelivery(id, 3)
	if should {
		t.Error("a second transport would be tried after an uncertain delivery")
	}
	got, _, _ := s.DeliveryStatus(id)
	if got.Transport != "inbox" {
		t.Errorf("Transport = %q, want the original recorded", got.Transport)
	}
}

// A recipient that could not be resolved resolves the same way next time, so a
// retry spends the budget on a configuration error and then presents it as a
// transport fault. Recorded as itself, and not retried.
func TestNoRecipientIsNotRetried(t *testing.T) {
	s := openTemp(t)
	id := openIncident(t, s)
	_ = s.RecordDelivery(id, "claude-inbox", OutcomeNoRecipient, t0)

	should, _ := s.ShouldAttemptDelivery(id, 3)
	if should {
		t.Error("delivery was retried when there was no recipient to deliver to")
	}
	got, _, _ := s.DeliveryStatus(id)
	if got.Outcome != OutcomeNoRecipient {
		t.Errorf("Outcome = %q, want it recorded as no_recipient rather than a failure", got.Outcome)
	}
}
