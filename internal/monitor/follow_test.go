package monitor

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/store"
)

// followingObserver reports the session under a new id once the target names
// the old one, the way the Claude observer does after /clear.
type followingObserver struct {
	from, to string
	asked    []string
}

func (f *followingObserver) Observe(t config.Target) (Sighting, error) {
	f.asked = append(f.asked, t.Attachment.SessionID)
	s := sighting(policy.StatusIdle, f.to+"@P", t0)
	if t.Attachment.SessionID == f.from {
		s.Follow = &Follow{FromSessionID: f.from, ToSessionID: f.to, PID: 100, ProcStart: "P", Via: FollowViaProcess}
	}
	return s, nil
}

func TestFollowMovesTheTargetAndSaysSo(t *testing.T) {
	obs := &followingObserver{from: "s", to: "s2"}
	m, st := newMonitor(t, obs)

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := m.cfg.Targets[0].Attachment; got.SessionID != "s2" || got.PID != 100 || got.ProcStart != "P" {
		t.Errorf("attachment = %+v, want it moved to s2 with the process identity", got)
	}

	events, err := st.After(0, 10)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) != 2 || events[0].Type != EventSessionFollowed || events[1].Type != "observation" {
		t.Fatalf("events = %+v, want session_followed then the observation of the new session", events)
	}
	var ev map[string]any
	if err := json.Unmarshal(events[0].Evidence, &ev); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if ev["from_session_id"] != "s" || ev["to_session_id"] != "s2" || ev["via"] != "process" {
		t.Errorf("evidence = %v, want from s to s2 via process", ev)
	}
	if events[0].Generation != events[1].Generation {
		t.Errorf("follow generation %d, observation generation %d: the follow announces the run it is observed under",
			events[0].Generation, events[1].Generation)
	}

	// The next pass asks about the new id and follows nothing further.
	if err := m.Tick(t0.Add(2e9)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if last := obs.asked[len(obs.asked)-1]; last != "s2" {
		t.Errorf("second pass observed %q, want s2", last)
	}
	follows, _ := st.Follows()
	if len(follows) != 1 {
		t.Errorf("follows = %+v, want exactly one recorded", follows)
	}

	open, _ := st.OpenIncidents("worker-1")
	for _, inc := range open {
		if inc.Condition == config.ConditionExit || inc.Condition == config.ConditionObservationLost {
			t.Errorf("open %s incident: a followed session is neither gone nor lost", inc.Condition)
		}
	}
}

// A restarted instance is configured with the id the file names. The
// recorded follow points it at the session it had been following before it
// asks anything.
func TestRecordedFollowsAreReplayedByANewMonitor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	cfg := func() *config.Config {
		return &config.Config{SchemaVersion: 1, Name: "i", Targets: []config.Target{{
			ID: "worker-1", Provider: "fake",
			Attachment: config.Attachment{Kind: "existing-session", SessionID: "s"},
			Policy:     config.Policy{QuietAfter: 1e12},
		}}}
	}

	first := &followingObserver{from: "s", to: "s2"}
	if err := New("i", cfg(), st, map[string]Observer{"fake": first}).Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	second := &followingObserver{from: "s", to: "s2"}
	m := New("i", cfg(), st, map[string]Observer{"fake": second})
	if err := m.Tick(t0.Add(2e9)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(second.asked) != 1 || second.asked[0] != "s2" {
		t.Errorf("restarted monitor asked about %v, want only s2", second.asked)
	}
	follows, _ := st.Follows()
	if len(follows) != 1 {
		t.Errorf("follows = %d, want 1: replaying a follow is not a new one", len(follows))
	}
}

// Follows chain: a session cleared twice is followed to its latest id, and a
// cycle cannot loop.
func TestResolveFollowsChainsAndStops(t *testing.T) {
	m := &Monitor{follows: map[string]map[string]store.Follow{"w": {
		"a": {TargetID: "w", FromSessionID: "a", ToSessionID: "b"},
		"b": {TargetID: "w", FromSessionID: "b", ToSessionID: "c"},
		"c": {TargetID: "w", FromSessionID: "c", ToSessionID: "a"},
	}}}
	target := config.Target{ID: "w", Attachment: config.Attachment{SessionID: "a"}}
	if !m.ResolveFollows(&target) {
		t.Fatal("ResolveFollows = false, want a change")
	}
	if target.Attachment.SessionID == "" {
		t.Error("session id emptied")
	}

	plain := config.Target{ID: "other", Attachment: config.Attachment{SessionID: "a"}}
	if m.ResolveFollows(&plain) {
		t.Error("followed a target with no recorded follows")
	}
}
