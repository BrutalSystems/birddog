package monitor

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/notify"
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
	// The configured id is asked about first, because it may have come back;
	// it has not, so the recorded follow applies.
	if len(second.asked) != 2 || second.asked[1] != "s2" {
		t.Errorf("restarted monitor asked about %v, want s then s2", second.asked)
	}
	if got := m.cfg.Targets[0].Attachment.SessionID; got != "s2" {
		t.Errorf("target follows %q, want s2", got)
	}
	if err := m.Tick(t0.Add(4e9)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(second.asked) != 3 || second.asked[2] != "s2" {
		t.Errorf("asked %v, want only s2 once the follow is applied", second.asked)
	}
	follows, _ := st.Follows()
	if len(follows) != 1 {
		t.Errorf("follows = %d, want 1: replaying a follow is not a new one", len(follows))
	}
}

// liveObserver reports whichever id it is told is live, and follows nothing.
type liveObserver struct {
	live  string
	asked []string
}

func (l *liveObserver) Observe(t config.Target) (Sighting, error) {
	l.asked = append(l.asked, t.Attachment.SessionID)
	if t.Attachment.SessionID == l.live {
		return sighting(policy.StatusIdle, l.live+"@Q", t0), nil
	}
	return Sighting{Observation: policy.Observation{Status: policy.StatusUnknown}}, nil
}

// A conversation resumed under the id it was once followed away from, in a
// new process, is live under exactly the id configured. The old follow must
// not pull the target back to the process that has gone.
func TestARecordedFollowDoesNotOverrideAConfiguredSessionThatIsLive(t *testing.T) {
	obs := &liveObserver{live: "s"}
	m, st := newMonitor(t, obs)
	if _, err := st.RecordFollow(store.Follow{TargetID: "worker-1", FromSessionID: "s", ToSessionID: "s2", PID: 100, ProcStart: "P", At: t0},
		store.Event{InstanceID: "inst-1", TargetID: "worker-1", Type: EventSessionFollowed, Source: "fake", At: t0}); err != nil {
		t.Fatal(err)
	}

	if err := m.Tick(t0.Add(2e9)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := m.cfg.Targets[0].Attachment.SessionID; got != "s" {
		t.Errorf("target follows %q, want s: it is live as configured", got)
	}
	open, _ := st.OpenIncidents("worker-1")
	for _, inc := range open {
		if inc.Condition == config.ConditionExit {
			t.Error("exit opened for a session that is running")
		}
	}
}

// A -> B and then B -> A in the same process is a session back where it
// started. The first link is stale once the second is recorded, so nothing
// is followed again on later passes.
func TestFollowingBackDoesNotCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ev := store.Event{InstanceID: "i", TargetID: "w", Type: EventSessionFollowed, Source: "fake", At: t0}
	_, _ = st.RecordFollow(store.Follow{TargetID: "w", FromSessionID: "a", ToSessionID: "b", At: t0}, ev)
	_, _ = st.RecordFollow(store.Follow{TargetID: "w", FromSessionID: "b", ToSessionID: "a", At: t0.Add(1e9)}, ev)

	follows, _ := st.Follows()
	if len(follows) != 1 || follows[0].FromSessionID != "b" {
		t.Fatalf("follows = %+v, want only b -> a", follows)
	}

	m := &Monitor{follows: map[string]map[string]store.Follow{}}
	for _, f := range follows {
		m.rememberFollow(f)
	}
	target := config.Target{ID: "w", Attachment: config.Attachment{SessionID: "a"}}
	if m.ResolveFollows(&target) {
		t.Errorf("resolved a target already at its current id to %q", target.Attachment.SessionID)
	}
}

// A target naming its own process, not the one a follow was made in, was
// pointed there on purpose.
func TestResolveFollowsLeavesATargetNamingAnotherProcess(t *testing.T) {
	m := &Monitor{follows: map[string]map[string]store.Follow{"w": {
		"a": {TargetID: "w", FromSessionID: "a", ToSessionID: "b", PID: 1, ProcStart: "P"},
	}}}
	target := config.Target{ID: "w", Attachment: config.Attachment{SessionID: "a", PID: 99, ProcStart: "Q"}}
	if m.ResolveFollows(&target) || target.Attachment.SessionID != "a" || target.Attachment.PID != 99 {
		t.Errorf("target = %+v, want it left as configured", target.Attachment)
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

// failingObserver cannot read its source from the very first pass.
type failingObserver struct{}

func (failingObserver) Observe(config.Target) (Sighting, error) {
	return Sighting{Observation: policy.Observation{
		Status: policy.StatusUnknown, Reason: policy.ReasonSourceUnreadable,
	}}, errors.New("registry unreadable")
}

// An alert about a target that has never been observed says so by saying
// nothing it did not see: no state line, and no "last observation" — which a
// stored zero once rendered as 1970-01-01.
func TestAnAlertForATargetNeverObservedClaimsNoObservation(t *testing.T) {
	n := &notify.Fake{Outcome: notify.Delivered}
	m, st := newMonitorWithNotifier(t, failingObserver{}, n)
	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if state, ok, _ := st.TargetState("worker-1"); !ok || state.Reason != policy.ReasonSourceUnreadable || !state.ObservedAt.IsZero() {
		t.Fatalf("state = %+v, want only the reason and no observed time", state)
	}
	if len(n.Delivered) == 0 {
		t.Fatal("no alert delivered for observation_lost")
	}
	text := n.Delivered[0].Text()
	for _, never := range []string{"State:", "Last observation", "1970"} {
		if strings.Contains(text, never) {
			t.Errorf("alert claims an observation (%q):\n%s", never, text)
		}
	}
}
