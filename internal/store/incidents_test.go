package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFirstObservationOpensAnIncident(t *testing.T) {
	s := openTemp(t)

	inc, opened, err := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)
	if err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	if !opened {
		t.Error("opened = false, want true for the first observation")
	}
	if inc.Condition != "idle" || inc.TargetID != "worker-1" {
		t.Errorf("incident = %+v", inc)
	}
	if !inc.OpenedAt.Equal(t0) {
		t.Errorf("OpenedAt = %v, want %v", inc.OpenedAt, t0)
	}
}

// Criterion 17: repeated observations of the same condition are one incident.
// Opening a second would turn a single idle worker into an alert storm.
func TestRepeatedObservationsTouchOneIncident(t *testing.T) {
	s := openTemp(t)
	first, _, _ := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)

	for i := 1; i <= 5; i++ {
		inc, opened, err := s.OpenOrTouchIncident("worker-1", "idle", 1, t0.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("OpenOrTouchIncident: %v", err)
		}
		if opened {
			t.Fatalf("observation %d opened a second incident", i)
		}
		if inc.ID != first.ID {
			t.Fatalf("incident id changed: %d then %d", first.ID, inc.ID)
		}
	}

	open, err := s.OpenIncidents("worker-1")
	if err != nil {
		t.Fatalf("OpenIncidents: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("got %d open incidents, want 1", len(open))
	}
	// Repeated observation is what keeps the incident's freshness current.
	if !open[0].LastSeenAt.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("LastSeenAt = %v, want the most recent observation", open[0].LastSeenAt)
	}
	if !open[0].OpenedAt.Equal(t0) {
		t.Errorf("OpenedAt = %v, want the first observation", open[0].OpenedAt)
	}
}

// Criterion 17 explicitly covers restart: incident state that lived only in
// memory would re-alert for every condition the moment birddog came back.
func TestIncidentDeduplicationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "birddog.db")

	s, _ := Open(path)
	first, _, _ := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	inc, opened, err := reopened.OpenOrTouchIncident("worker-1", "idle", 1, t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("OpenOrTouchIncident after restart: %v", err)
	}
	if opened {
		t.Error("opened = true after restart, want false — the incident was already open")
	}
	if inc.ID != first.ID {
		t.Errorf("incident id = %d, want the pre-restart %d", inc.ID, first.ID)
	}
}

func TestDifferentConditionsAreSeparateIncidents(t *testing.T) {
	s := openTemp(t)
	idle, _, _ := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)

	quiet, opened, err := s.OpenOrTouchIncident("worker-1", "quiet", 1, t0)
	if err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	if !opened {
		t.Error("opened = false, want true — quiet is a different condition")
	}
	if quiet.ID == idle.ID {
		t.Error("quiet and idle share an incident id")
	}
}

func TestDifferentTargetsAreSeparateIncidents(t *testing.T) {
	s := openTemp(t)
	_, _, _ = s.OpenOrTouchIncident("worker-1", "idle", 1, t0)

	_, opened, err := s.OpenOrTouchIncident("worker-2", "idle", 1, t0)
	if err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	if !opened {
		t.Error("opened = false, want true — another target's incident is not this one")
	}
}

// One incident per target *and run generation*: a replacement session going
// idle is a new fact about a new session, not a continuation.
func TestNewGenerationOpensANewIncident(t *testing.T) {
	s := openTemp(t)
	first, _, _ := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)

	second, opened, err := s.OpenOrTouchIncident("worker-1", "idle", 2, t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	if !opened {
		t.Error("opened = false, want true for a new run generation")
	}
	if second.ID == first.ID {
		t.Error("new generation reused the old incident")
	}
}

func TestResolvingClosesTheIncident(t *testing.T) {
	s := openTemp(t)
	inc, _, _ := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)

	if err := s.ResolveIncident(inc.ID, t0.Add(time.Minute)); err != nil {
		t.Fatalf("ResolveIncident: %v", err)
	}

	open, err := s.OpenIncidents("worker-1")
	if err != nil {
		t.Fatalf("OpenIncidents: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("got %d open incidents, want 0", len(open))
	}
}

// The condition recurring after it resolved is a new incident, not a reopening
// of the old one — the orchestrator needs to hear about it again.
func TestConditionRecurringAfterResolutionOpensANewIncident(t *testing.T) {
	s := openTemp(t)
	first, _, _ := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)
	_ = s.ResolveIncident(first.ID, t0.Add(time.Minute))

	second, opened, err := s.OpenOrTouchIncident("worker-1", "idle", 1, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("OpenOrTouchIncident: %v", err)
	}
	if !opened {
		t.Error("opened = false, want true — the condition recurred after resolving")
	}
	if second.ID == first.ID {
		t.Error("recurrence reused the resolved incident's id")
	}
}

// Acknowledging records that the orchestrator saw the alert. It must not
// resolve the incident: the worker is still waiting, whatever anyone has read.
func TestAcknowledgingDoesNotResolveTheIncident(t *testing.T) {
	s := openTemp(t)
	inc, _, _ := s.OpenOrTouchIncident("worker-1", "input_requested", 1, t0)

	if err := s.AcknowledgeIncident(inc.ID, t0.Add(time.Minute)); err != nil {
		t.Fatalf("AcknowledgeIncident: %v", err)
	}

	open, err := s.OpenIncidents("worker-1")
	if err != nil {
		t.Fatalf("OpenIncidents: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("got %d open incidents, want 1 — acknowledging is not resolving", len(open))
	}
	if open[0].AcknowledgedAt == nil {
		t.Error("AcknowledgedAt not recorded")
	}
}

// Notification is tracked per incident so an orchestrator is told once when a
// condition opens, rather than on every repeat observation.
func TestIncidentRecordsWhetherItHasBeenNotified(t *testing.T) {
	s := openTemp(t)
	inc, _, _ := s.OpenOrTouchIncident("worker-1", "idle", 1, t0)
	if inc.NotifiedAt != nil {
		t.Error("a freshly opened incident is already marked notified")
	}

	if err := s.MarkNotified(inc.ID, t0.Add(time.Second)); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	open, _ := s.OpenIncidents("worker-1")
	if open[0].NotifiedAt == nil {
		t.Fatal("NotifiedAt not recorded")
	}

	// A repeat observation must not clear it and cause a second notification.
	_, _, _ = s.OpenOrTouchIncident("worker-1", "idle", 1, t0.Add(time.Minute))
	open, _ = s.OpenIncidents("worker-1")
	if open[0].NotifiedAt == nil {
		t.Error("a repeat observation cleared NotifiedAt — that re-notifies for an unchanged condition")
	}
}
