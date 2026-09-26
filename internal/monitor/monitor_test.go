package monitor

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/notify"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/store"
)

var t0 = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// fakeObserver returns whatever the test sets, so the loop is exercised
// without any real session.
type fakeObserver struct {
	sighting Sighting
	err      error
	calls    int
}

func (f *fakeObserver) Observe(config.Target) (Sighting, error) {
	f.calls++
	return f.sighting, f.err
}

func sighting(status string, identity string, at time.Time) Sighting {
	return Sighting{
		Observation: policy.Observation{
			Live: true, Status: status, StatusKnown: true,
			StatusSince: at, LastActivityAt: at, ObservedAt: at,
		},
		SessionIdentity: identity,
	}
}

func newMonitor(t *testing.T, obs Observer, policyMods ...func(*config.Policy)) (*Monitor, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	p := config.Policy{
		AlertOn: []string{
			config.ConditionIdle, config.ConditionInputRequested, config.ConditionExit,
			config.ConditionQuiet, config.ConditionObservationLost,
		},
		QuietAfter: 5 * time.Minute,
		IdleGrace:  30 * time.Second,
	}
	for _, mod := range policyMods {
		mod(&p)
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Name:          "test-instance",
		Targets: []config.Target{{
			ID: "worker-1", Provider: "fake",
			Attachment: config.Attachment{Kind: "existing-session", SessionID: "s"},
			Policy:     p,
		}},
	}
	return New("inst-1", cfg, st, map[string]Observer{"fake": obs}), st
}

func TestTickRecordsAnObservationAsState(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{sighting: sighting(policy.StatusActive, "session-a", t0)})

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, ok, err := st.TargetState("worker-1")
	if err != nil || !ok {
		t.Fatalf("TargetState: %v ok=%v", err, ok)
	}
	if got.Status != policy.StatusActive {
		t.Errorf("Status = %q, want active", got.Status)
	}
}

func TestTickOpensAnIncidentWhenTheConditionHolds(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)})

	if err := m.Tick(t0.Add(time.Second)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	open, err := st.OpenIncidents("worker-1")
	if err != nil {
		t.Fatalf("OpenIncidents: %v", err)
	}
	if len(open) != 1 || open[0].Condition != config.ConditionInputRequested {
		t.Fatalf("open incidents = %+v, want one input_requested", open)
	}
}

// The loop runs continuously; a condition that persists must stay one incident.
func TestRepeatedTicksDoNotDuplicateAnIncident(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)})

	for i := range 10 {
		if err := m.Tick(t0.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}

	open, _ := st.OpenIncidents("worker-1")
	if len(open) != 1 {
		t.Errorf("got %d open incidents after 10 ticks, want 1", len(open))
	}
}

func TestConditionEndingResolvesTheIncident(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, st := newMonitor(t, obs)

	_ = m.Tick(t0)
	if open, _ := st.OpenIncidents("worker-1"); len(open) != 1 {
		t.Fatalf("expected an open incident before resolution")
	}

	// The human answered; the session is working again.
	obs.sighting = sighting(policy.StatusActive, "session-a", t0.Add(time.Minute))
	if err := m.Tick(t0.Add(time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	open, _ := st.OpenIncidents("worker-1")
	if len(open) != 0 {
		t.Errorf("open incidents = %+v, want the input request resolved", open)
	}
}

// Criterion 16 end to end: a replacement session is a new generation, so the
// old session's state cannot describe it.
func TestReplacedSessionStartsANewGeneration(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusActive, "session-a", t0)}
	m, st := newMonitor(t, obs)

	_ = m.Tick(t0)
	first, _, _ := st.TargetState("worker-1")

	obs.sighting = sighting(policy.StatusActive, "session-b", t0.Add(time.Minute))
	_ = m.Tick(t0.Add(time.Minute))

	second, _, _ := st.TargetState("worker-1")
	if second.Generation <= first.Generation {
		t.Errorf("generation did not advance for a replacement session: %d then %d",
			first.Generation, second.Generation)
	}
}

func TestUnchangedSessionKeepsItsGeneration(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusActive, "session-a", t0)}
	m, st := newMonitor(t, obs)

	_ = m.Tick(t0)
	first, _, _ := st.TargetState("worker-1")

	obs.sighting = sighting(policy.StatusIdle, "session-a", t0.Add(time.Minute))
	_ = m.Tick(t0.Add(time.Minute))

	second, _, _ := st.TargetState("worker-1")
	if second.Generation != first.Generation {
		t.Errorf("generation changed without the session changing: %d then %d",
			first.Generation, second.Generation)
	}
}

// A replacement session going idle is a new fact, so it gets its own incident
// rather than inheriting the old session's.
func TestNewGenerationOpensItsOwnIncident(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, st := newMonitor(t, obs)

	_ = m.Tick(t0)
	before, _ := st.OpenIncidents("worker-1")

	obs.sighting = sighting(policy.StatusWaitingInput, "session-b", t0.Add(time.Minute))
	_ = m.Tick(t0.Add(time.Minute))

	after, _ := st.OpenIncidents("worker-1")
	if len(after) == 0 {
		t.Fatal("no incident for the replacement session")
	}
	if after[len(after)-1].ID == before[0].ID {
		t.Error("the replacement session reused the previous session's incident")
	}
}

// An adapter that fails is a loss of observation, not a crash and not an exit.
func TestObserverFailureBecomesObservationLost(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{err: errors.New("app-server gone")})

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick returned an error for an adapter failure: %v — one bad target must not stop the pass", err)
	}

	open, _ := st.OpenIncidents("worker-1")
	if len(open) != 1 || open[0].Condition != config.ConditionObservationLost {
		t.Fatalf("open incidents = %+v, want one observation_lost", open)
	}
}

func TestObserverFailureDoesNotOverwriteLastKnownState(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusActive, "session-a", t0)}
	m, st := newMonitor(t, obs)
	_ = m.Tick(t0)

	obs.err = errors.New("gone")
	_ = m.Tick(t0.Add(time.Minute))

	got, _, _ := st.TargetState("worker-1")
	if got.Status != policy.StatusActive {
		t.Errorf("Status = %q, want the last observed state preserved rather than overwritten", got.Status)
	}
}

// A target with no configured observer is a capability gap, reported honestly
// rather than silently skipped.
func TestUnknownProviderIsReportedAsObservationLost(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{sighting: sighting(policy.StatusActive, "s", t0)})
	m.cfg.Targets[0].Provider = "opencode" // no observer registered

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	open, _ := st.OpenIncidents("worker-1")
	if len(open) != 1 || open[0].Condition != config.ConditionObservationLost {
		t.Errorf("open incidents = %+v, want observation_lost for an unobservable provider", open)
	}
}

// One failing target must not stop the others being observed.
func TestOneFailingTargetDoesNotStopThePass(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{err: errors.New("boom")})
	m.cfg.Targets = append(m.cfg.Targets, config.Target{
		ID: "worker-2", Provider: "healthy",
		Attachment: config.Attachment{Kind: "existing-session", SessionID: "s2"},
		Policy:     m.cfg.Targets[0].Policy,
	})
	m.observers["healthy"] = &fakeObserver{sighting: sighting(policy.StatusActive, "session-b", t0)}

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, ok, _ := st.TargetState("worker-2"); !ok {
		t.Error("the healthy target was never observed because another one failed")
	}
}

// Every observation is recorded, so the event feed shows what was seen even
// when nothing about the state changed.
func TestTicksAppendToTheEventFeed(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{sighting: sighting(policy.StatusActive, "session-a", t0)})

	_ = m.Tick(t0)
	_ = m.Tick(t0.Add(time.Second))

	events, err := st.After(0, 100)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) < 2 {
		t.Errorf("got %d events from two ticks, want at least 2", len(events))
	}
	for _, e := range events {
		if e.InstanceID != "inst-1" {
			t.Errorf("event carries instance %q, want inst-1 so overlapping watches stay distinguishable", e.InstanceID)
		}
	}
}

// Labels ride along as correlation metadata and change nothing.
func TestLabelsAreCarriedOntoEventsWithoutInterpretation(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{sighting: sighting(policy.StatusActive, "session-a", t0)})
	m.cfg.Targets[0].Labels = map[string]string{"task_id": "T-42"}

	_ = m.Tick(t0)

	events, _ := st.After(0, 10)
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	if !contains(string(events[0].Evidence), "T-42") {
		t.Errorf("evidence = %s, want the opaque label carried through", events[0].Evidence)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(haystack) > 0 && (stringIndex(haystack, needle) >= 0))
}

func stringIndex(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// Criterion 10, through the loop: after a detected wake the monitor must not
// let thresholds reach back across the sleep.
func TestMonitorAppliesAThresholdFloorAfterAWake(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusActive, "session-a", t0)}
	m, st := newMonitor(t, obs)

	// A pass before the sleep establishes a baseline.
	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// The next pass is eight hours later by the wall clock but only moments
	// later by the monotonic clock: the machine slept.
	m.SetElapsed(func() time.Duration { return 2 * time.Second })
	wake := t0.Add(8 * time.Hour)
	if err := m.Tick(wake); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	open, err := st.OpenIncidents("worker-1")
	if err != nil {
		t.Fatalf("OpenIncidents: %v", err)
	}
	for _, inc := range open {
		if inc.Condition == config.ConditionQuiet {
			t.Error("quiet opened on the first pass after waking — eight hours nobody was watching")
		}
	}
}

// Ordinary passes must not be mistaken for wakes, or thresholds reset forever
// and quiet never fires at all.
func TestOrdinaryPassesDoNotTriggerAWakeFloor(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusUnknown, "session-a", t0)}
	obs.sighting.StatusKnown = false
	m, st := newMonitor(t, obs)

	elapsed := time.Duration(0)
	m.SetElapsed(func() time.Duration { return elapsed })

	// Ten minutes of passes, both clocks advancing together.
	for i := range 10 {
		elapsed = time.Duration(i) * time.Minute
		if err := m.Tick(t0.Add(time.Duration(i) * time.Minute)); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}

	open, _ := st.OpenIncidents("worker-1")
	found := false
	for _, inc := range open {
		if inc.Condition == config.ConditionQuiet {
			found = true
		}
	}
	if !found {
		t.Error("quiet never opened across ten minutes of ordinary passes — a wake floor is being applied when nothing jumped")
	}
}

func newMonitorWithNotifier(t *testing.T, obs Observer, n *notify.Fake) (*Monitor, *store.Store) {
	t.Helper()
	m, st := newMonitor(t, obs)
	m.SetNotifier(n)
	return m, st
}

func TestOpeningAnIncidentNotifiesOnce(t *testing.T) {
	n := &notify.Fake{Outcome: notify.Delivered}
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, _ := newMonitorWithNotifier(t, obs, n)

	for i := range 5 {
		if err := m.Tick(t0.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}

	if len(n.Delivered) != 1 {
		t.Errorf("delivered %d alerts for one persisting condition, want 1", len(n.Delivered))
	}
}

func TestDeliveredAlertCarriesTheIncidentAndInstance(t *testing.T) {
	n := &notify.Fake{Outcome: notify.Delivered}
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, _ := newMonitorWithNotifier(t, obs, n)
	_ = m.Tick(t0)

	if len(n.Delivered) != 1 {
		t.Fatalf("delivered %d alerts, want 1", len(n.Delivered))
	}
	a := n.Delivered[0]
	if a.InstanceID != "inst-1" || a.TargetID != "worker-1" {
		t.Errorf("alert = %+v, want it to name its instance and target", a)
	}
	if a.IncidentID == 0 {
		t.Error("alert carries no incident id, so it cannot be acknowledged")
	}
}

// Criterion 18: an uncertain delivery is recorded and not repeated.
func TestUncertainDeliveryIsNotRepeated(t *testing.T) {
	n := &notify.Fake{Outcome: notify.Unknown}
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, st := newMonitorWithNotifier(t, obs, n)

	for i := range 5 {
		_ = m.Tick(t0.Add(time.Duration(i) * time.Second))
	}

	if len(n.Delivered) != 1 {
		t.Errorf("attempted delivery %d times after an uncertain result, want 1", len(n.Delivered))
	}
	d, ok, err := st.DeliveryStatus(1)
	if err != nil || !ok {
		t.Fatalf("DeliveryStatus: %v ok=%v", err, ok)
	}
	if d.Outcome != store.OutcomeUnknown {
		t.Errorf("Outcome = %q, want it recorded as uncertain", d.Outcome)
	}
}

// A definite failure is retried, because nothing arrived.
func TestDefiniteFailureIsRetried(t *testing.T) {
	n := &notify.Fake{Outcome: notify.Failed, Err: errors.New("no route")}
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, _ := newMonitorWithNotifier(t, obs, n)

	for i := range 5 {
		_ = m.Tick(t0.Add(time.Duration(i) * time.Second))
	}

	if len(n.Delivered) < 2 {
		t.Errorf("attempted delivery %d times after definite failures, want retries", len(n.Delivered))
	}
	if len(n.Delivered) > 3 {
		t.Errorf("attempted delivery %d times, want retries bounded", len(n.Delivered))
	}
}

// Criterion 18: the event feed is unaffected by anything the transport does.
func TestNotificationFailureDoesNotAffectObservation(t *testing.T) {
	n := &notify.Fake{Outcome: notify.Failed, Err: errors.New("no route")}
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, st := newMonitorWithNotifier(t, obs, n)

	for i := range 3 {
		if err := m.Tick(t0.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatalf("Tick: %v — a failing transport must not stop observation", err)
		}
	}

	events, _ := st.After(0, 100)
	if len(events) < 3 {
		t.Errorf("got %d events, want observation unaffected by delivery failures", len(events))
	}
	open, _ := st.OpenIncidents("worker-1")
	if len(open) != 1 {
		t.Errorf("open incidents = %d, want the alert still available to poll", len(open))
	}
}

// With no transport configured, nothing is attempted and everything is still
// recorded — the default, and the fallback whenever delivery is unavailable.
func TestWithNoTransportAlertsStayInTheFeed(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, st := newMonitor(t, obs) // no notifier set: None
	_ = m.Tick(t0)

	open, _ := st.OpenIncidents("worker-1")
	if len(open) != 1 {
		t.Errorf("open incidents = %d, want the condition recorded regardless of transport", len(open))
	}
}

// Learning a session's identity for the first time is not a new run.
//
// An unobservable target has no identity to record. When it becomes
// observable, treating the identity it now reports as a *change* bumps the
// generation — which orphans the observation-lost incident raised moments
// earlier, because incidents resolve only within their own generation. The
// target then reports a condition that has demonstrably ended, forever.
func TestBecomingObservableDoesNotStartANewGeneration(t *testing.T) {
	obs := &fakeObserver{err: errors.New("not publishing yet")}
	m, st := newMonitor(t, obs)

	// Two unobservable passes, then it appears. A failed observation asserts
	// no status, so no state row exists until the third pass writes one.
	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := m.Tick(t0.Add(time.Second)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	obs.err = nil
	obs.sighting = sighting(policy.StatusIdle, "session-a", t0.Add(2*time.Second))
	if err := m.Tick(t0.Add(2 * time.Second)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	after, ok, _ := st.TargetState("worker-1")
	if !ok {
		t.Fatal("no state recorded once the session appeared")
	}
	// The first run this target has had, not the second: nothing was ever
	// superseded, so an incident raised while it was unobservable is still
	// this generation's and can still be resolved.
	if after.Generation != 1 {
		t.Errorf("generation = %d, want 1 — learning an identity is not a new run", after.Generation)
	}
}

func TestObservationLostResolvesWhenTheSessionAppears(t *testing.T) {
	obs := &fakeObserver{err: errors.New("not publishing yet")}
	m, st := newMonitor(t, obs)

	_ = m.Tick(t0)
	if open, _ := st.OpenIncidents("worker-1"); len(open) != 1 {
		t.Fatalf("expected observation_lost to be open, got %+v", open)
	}

	obs.err = nil
	obs.sighting = sighting(policy.StatusIdle, "session-a", t0.Add(time.Second))
	_ = m.Tick(t0.Add(time.Second))

	open, _ := st.OpenIncidents("worker-1")
	for _, inc := range open {
		if inc.Condition == config.ConditionObservationLost {
			t.Error("observation_lost still open after the session became observable")
		}
	}
}

// Belt and braces: an incident belonging to a run that has been superseded is
// closed out rather than left open for a session that no longer exists.
func TestIncidentsFromASupersededRunAreResolved(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "session-a", t0)}
	m, st := newMonitor(t, obs)
	_ = m.Tick(t0)

	// A genuinely different session takes the target over, and is not
	// waiting on anything.
	obs.sighting = sighting(policy.StatusActive, "session-b", t0.Add(time.Minute))
	_ = m.Tick(t0.Add(time.Minute))

	open, _ := st.OpenIncidents("worker-1")
	for _, inc := range open {
		if inc.Generation < 2 {
			t.Errorf("incident from the superseded run is still open: %+v", inc)
		}
	}
}

// Audit of the delivery path (issue #16): a hand-off that fails must not
// advance a position past what it failed to hand off, or the loss becomes
// invisible — the next read starts after the missing item with nothing to say
// it was skipped.
//
// birddog holds the property by construction rather than by care: delivery
// writes only to the deliveries table, keyed by incident, and the event cursor
// is advanced solely by recording an observation. There is no delivery cursor
// to get wrong.
//
// Pinned here because that is an architectural property rather than an obvious
// one, and a future connector appending its own receipts to the feed would
// quietly acquire the hazard.
func TestAFailedDeliveryDoesNotAdvanceTheEventCursor(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusWaitingInput, "s1", t0)}
	m, st := newMonitor(t, obs)
	n := &notify.Fake{Outcome: notify.Failed}
	m.SetNotifier(n)

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	before, err := st.LatestCursor()
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}

	for i := 1; i <= 3; i++ {
		if err := m.Tick(t0.Add(time.Duration(i) * time.Minute)); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	after, err := st.LatestCursor()
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}

	if len(n.Delivered) < 2 {
		t.Fatalf("deliveries attempted = %d, want the failures retried so the case is real", len(n.Delivered))
	}
	// The cursor moved by exactly the observations recorded, and not at all by
	// the deliveries that failed underneath them.
	if got := after - before; got != 3 {
		t.Errorf("cursor advanced by %d across 3 observations with failed deliveries, want 3", got)
	}
}

// openCondition reports whether a condition is currently open for a target.
func openCondition(t *testing.T, st *store.Store, targetID, condition string) bool {
	t.Helper()
	open, err := st.OpenIncidents(targetID)
	if err != nil {
		t.Fatalf("OpenIncidents: %v", err)
	}
	for _, inc := range open {
		if inc.Condition == condition {
			return true
		}
	}
	return false
}

// A target whose observation was lost and regained must not immediately report
// quiet about the gap. The silence during the outage was not witnessed, so it
// is not a duration birddog may measure — the same rule it already applies
// after the machine wakes.
func TestRegainingObservationRestartsThresholds(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusActive, "s1", t0)}
	m, st := newMonitor(t, obs, func(p *config.Policy) {
		p.QuietAfter = 5 * time.Minute
	})

	// Both clocks advance together: this is an outage, not a sleep, and the
	// wake detector must not claim it.
	elapsed := time.Duration(0)
	m.SetElapsed(func() time.Duration { return elapsed })

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// The adapter breaks. Nothing is learned for twenty minutes.
	elapsed = 20 * time.Minute
	obs.sighting, obs.err = unobserved(), errors.New("adapter child died")
	if err := m.Tick(t0.Add(20 * time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// It comes back, reachable but with nothing new to report and an activity
	// timestamp still stuck at the last real sighting.
	elapsed = 40 * time.Minute
	recovered := Sighting{Observation: policy.Observation{
		Live: true, Status: policy.StatusUnknown, StatusKnown: false,
		LastActivityAt: t0, ObservedAt: t0.Add(40 * time.Minute),
	}, SessionIdentity: "s1"}
	obs.sighting, obs.err = recovered, nil
	if err := m.Tick(t0.Add(40 * time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if openCondition(t, st, "worker-1", config.ConditionQuiet) {
		t.Error("quiet opened the moment observation returned, about a gap birddog could not see")
	}
}

// The floor is a floor, not an amnesty: once the threshold has passed measured
// from the recovery, quiet applies again.
func TestThresholdsApplyAgainAfterTheRecoveryHorizon(t *testing.T) {
	obs := &fakeObserver{sighting: sighting(policy.StatusActive, "s1", t0)}
	m, st := newMonitor(t, obs, func(p *config.Policy) {
		p.QuietAfter = 5 * time.Minute
	})

	elapsed := time.Duration(0)
	m.SetElapsed(func() time.Duration { return elapsed })

	_ = m.Tick(t0)
	obs.sighting, obs.err = unobserved(), errors.New("adapter child died")
	elapsed = 20 * time.Minute
	_ = m.Tick(t0.Add(20 * time.Minute))

	recovery := t0.Add(40 * time.Minute)
	elapsed = 40 * time.Minute
	obs.sighting, obs.err = Sighting{Observation: policy.Observation{
		Live: true, Status: policy.StatusUnknown, StatusKnown: false,
		LastActivityAt: t0, ObservedAt: recovery,
	}, SessionIdentity: "s1"}, nil
	_ = m.Tick(recovery)
	elapsed = 50 * time.Minute
	_ = m.Tick(recovery.Add(10 * time.Minute))

	if !openCondition(t, st, "worker-1", config.ConditionQuiet) {
		t.Error("quiet never returned after the threshold passed from the recovery")
	}
}

// twoTargets builds a monitor watching two targets through separate observers,
// so a failure can hit one or both.
func twoTargets(t *testing.T, a, b Observer) (*Monitor, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	p := config.Policy{
		AlertOn: []string{
			config.ConditionQuiet, config.ConditionObservationLost, config.ConditionExit,
		},
		QuietAfter: 5 * time.Minute,
		IdleGrace:  30 * time.Second,
	}
	cfg := &config.Config{
		SchemaVersion: 1, Name: "test-instance",
		Targets: []config.Target{
			{ID: "worker-1", Provider: "a", Attachment: config.Attachment{Kind: "existing-session", SessionID: "s"}, Policy: p},
			{ID: "worker-2", Provider: "b", Attachment: config.Attachment{Kind: "existing-session", SessionID: "s"}, Policy: p},
		},
	}
	return New("inst-1", cfg, st, map[string]Observer{"a": a, "b": b}), st
}

func evidenceOf(t *testing.T, st *store.Store, targetID string) map[string]any {
	t.Helper()
	events, err := st.After(0, 1000)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].TargetID != targetID {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(events[i].Evidence, &m); err != nil {
			t.Fatalf("decode evidence: %v", err)
		}
		return m
	}
	t.Fatalf("no event for %s", targetID)
	return nil
}

// Every target going unobservable in the same pass is the shape a dead adapter
// child or an unreadable registry produces. The sessions are most likely fine
// and simply out of view, so it is recorded as one correlated fault rather
// than left to be inferred from N separate ones.
func TestLosingEveryTargetAtOnceIsRecordedAsCorrelated(t *testing.T) {
	a := &fakeObserver{sighting: unobserved(), err: errors.New("registry unreadable")}
	b := &fakeObserver{sighting: unobserved(), err: errors.New("registry unreadable")}
	m, st := twoTargets(t, a, b)

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	for _, id := range []string{"worker-1", "worker-2"} {
		if got := evidenceOf(t, st, id); got["correlated_loss"] != true {
			t.Errorf("%s evidence = %v, want the loss marked correlated", id, got)
		}
	}
}

// One target failing while another is observed says nothing about birddog, so
// it must not be dressed up as a fault of its own.
func TestLosingOneTargetIsNotCorrelated(t *testing.T) {
	a := &fakeObserver{sighting: unobserved(), err: errors.New("gone")}
	b := &fakeObserver{sighting: sighting(policy.StatusActive, "s2", t0)}
	m, st := twoTargets(t, a, b)

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := evidenceOf(t, st, "worker-1"); got["correlated_loss"] == true {
		t.Errorf("worker-1 evidence = %v, want no correlation claimed when another target was observed", got)
	}
}

// A verified exit is an observation, not a gap. A pass that proved one session
// ended and could not see the other has not lost its own view of everything.
func TestAVerifiedExitIsNotALostObservation(t *testing.T) {
	exited := sighting(policy.StatusExited, "s1", t0)
	exited.Live = false
	a := &fakeObserver{sighting: exited}
	b := &fakeObserver{sighting: unobserved(), err: errors.New("gone")}
	m, st := twoTargets(t, a, b)

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := evidenceOf(t, st, "worker-2"); got["correlated_loss"] == true {
		t.Errorf("evidence = %v, want no correlation: one target's exit was observed", got)
	}
}

// exitSighting is a verified exit as an adapter actually reports one: not live,
// and a known status all the same. The process is gone, which is why there is
// nothing to be in contact with and why the answer is still certain.
func exitSighting(identity string) Sighting {
	return Sighting{
		Observation: policy.Observation{
			Live: false, Status: policy.StatusExited, StatusKnown: true,
		},
		SessionIdentity: identity,
	}
}

func TestVerifiedExitIsRecordedOnce(t *testing.T) {
	obs := &fakeObserver{sighting: exitSighting("session-a")}
	m, st := newMonitor(t, obs)

	for i := 0; i < 5; i++ {
		if err := m.Tick(t0.Add(time.Duration(i) * 2 * time.Second)); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}

	events, err := st.After(0, 100)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1: re-observing a terminal fact asserts nothing new", len(events))
	}
	if events[0].Status != policy.StatusExited {
		t.Errorf("Status = %q, want exited", events[0].Status)
	}
	if obs.calls != 5 {
		t.Errorf("Observe called %d times, want 5: observation continues, only writing stops", obs.calls)
	}
}

func TestNoIncidentWriteAfterAVerifiedExit(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{sighting: exitSighting("session-a")})

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	open, err := st.OpenIncidents("worker-1")
	if err != nil || len(open) != 1 {
		t.Fatalf("OpenIncidents: %v len=%d", err, len(open))
	}
	first := open[0].LastSeenAt

	if err := m.Tick(t0.Add(time.Hour)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	open, err = st.OpenIncidents("worker-1")
	if err != nil || len(open) != 1 {
		t.Fatalf("OpenIncidents: %v len=%d", err, len(open))
	}
	if !open[0].LastSeenAt.Equal(first) {
		t.Errorf("LastSeenAt moved to %v, want frozen at %v: a condition that cannot lapse has nothing to refresh",
			open[0].LastSeenAt, first)
	}
}

func TestReplacementSessionAfterAnExitRecordsAgain(t *testing.T) {
	obs := &fakeObserver{sighting: exitSighting("session-a")}
	m, st := newMonitor(t, obs)

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := m.Tick(t0.Add(2 * time.Second)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	obs.sighting = sighting(policy.StatusActive, "session-b", t0.Add(4*time.Second))
	if err := m.Tick(t0.Add(4 * time.Second)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	events, err := st.After(0, 100)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("recorded %d events, want 2: a replacement session is a new run", len(events))
	}
	if events[1].Generation != 2 {
		t.Errorf("Generation = %d, want 2", events[1].Generation)
	}
}

func TestContactLostAfterAnExitRecordsAgain(t *testing.T) {
	obs := &fakeObserver{sighting: exitSighting("session-a")}
	m, st := newMonitor(t, obs)

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Not a restatement of the exit: birddog can no longer see the target at
	// all, which is a different answer and must still be recorded.
	obs.err = errors.New("registry unreadable")
	if err := m.Tick(t0.Add(2 * time.Second)); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	events, err := st.After(0, 100)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("recorded %d events, want 2: losing sight of an exited target is a change", len(events))
	}
}

// A pass that loses observation resolves the exit incident and opens
// observation_lost. Regaining sight of the exit must reopen it: the exit
// incident is the only thing `ack` can name, and once it is resolved
// `OpenIncidents` stops listing it, so an outcome nobody has collected becomes
// impossible to collect. Suppressing on the repeated exit alone left it that
// way forever.
func TestAnExitIncidentReopensAfterAnObservationGap(t *testing.T) {
	obs := &fakeObserver{sighting: exitSighting("session-a")}
	m, st := newMonitor(t, obs)

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick (exit): %v", err)
	}
	if !openCondition(t, st, "worker-1", config.ConditionExit) {
		t.Fatal("the exit incident did not open")
	}

	obs.err = errors.New("registry unreadable")
	if err := m.Tick(t0.Add(2 * time.Second)); err != nil {
		t.Fatalf("Tick (loss): %v", err)
	}
	if openCondition(t, st, "worker-1", config.ConditionExit) {
		t.Fatal("the exit incident survived the gap, so this test proves nothing")
	}
	if !openCondition(t, st, "worker-1", config.ConditionObservationLost) {
		t.Fatal("observation_lost did not open during the gap")
	}

	obs.err = nil
	for i := range 3 {
		if err := m.Tick(t0.Add(time.Duration(4+2*i) * time.Second)); err != nil {
			t.Fatalf("Tick (regain %d): %v", i, err)
		}
	}

	if !openCondition(t, st, "worker-1", config.ConditionExit) {
		t.Error("the exit incident is still resolved after the exit was observed again: nothing can ack it")
	}
	if openCondition(t, st, "worker-1", config.ConditionObservationLost) {
		t.Error("observation_lost is still open although the target is visibly exited")
	}

	// The repair costs exactly one pass. Everything after it is a genuine
	// restatement and is suppressed again.
	events, err := st.After(0, 100)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("recorded %d events, want 3: the exit, the gap, and the one pass that repaired it", len(events))
	}
}

// The exit alert is the one birddog most needs to land, and applyDecision is
// the only thing that retries a failed delivery. A pass suppressed as a
// terminal repeat never reaches it, so a transient transport failure on an
// exited target would be final.
func TestAFailedExitAlertIsRetriedAcrossSuppressedPasses(t *testing.T) {
	n := &notify.Fake{Outcome: notify.Failed, Err: errors.New("no route")}
	obs := &fakeObserver{sighting: exitSighting("session-a")}
	m, st := newMonitorWithNotifier(t, obs, n)

	for i := range 6 {
		if err := m.Tick(t0.Add(time.Duration(i) * 2 * time.Second)); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}

	if len(n.Delivered) != defaultDeliveryAttempts {
		t.Errorf("attempted delivery %d times for an exited target, want %d: a repeat must not cost the retries",
			len(n.Delivered), defaultDeliveryAttempts)
	}
	open, err := st.OpenIncidents("worker-1")
	if err != nil || len(open) != 1 {
		t.Fatalf("OpenIncidents: %v len=%d", err, len(open))
	}
	d, ok, err := st.DeliveryStatus(open[0].ID)
	if err != nil || !ok {
		t.Fatalf("DeliveryStatus: %v ok=%v", err, ok)
	}
	if d.Attempts != defaultDeliveryAttempts {
		t.Errorf("Attempts = %d, want %d", d.Attempts, defaultDeliveryAttempts)
	}
}

func TestEveryEventCarriesTheActivityResolution(t *testing.T) {
	s := sighting(policy.StatusActive, "session-a", t0)
	s.ActivityResolution = policy.ResolutionActivity
	m, st := newMonitor(t, &fakeObserver{sighting: s})

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := evidenceOf(t, st, "worker-1")["activity_resolution"]; got != policy.ResolutionActivity {
		t.Errorf("activity_resolution = %v, want activity", got)
	}
}

// Review Focus 1. The qualifier must not live inside the existing
// !LastActivityAt.IsZero() guard: "no signal" is the case unavailable exists
// for, and a consumer must be able to tell it from a build that predates the
// field.
func TestAnObservationWithNoTimestampStillCarriesTheResolution(t *testing.T) {
	s := Sighting{Observation: policy.Observation{
		Live: true, Status: policy.StatusUnknown, StatusKnown: false,
		ActivityResolution: policy.ResolutionUnavailable,
	}}
	m, st := newMonitor(t, &fakeObserver{sighting: s})

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	ev := evidenceOf(t, st, "worker-1")
	if _, ok := ev["last_activity_at"]; ok {
		t.Error("last_activity_at present, want absent for this sighting")
	}
	if got := ev["activity_resolution"]; got != policy.ResolutionUnavailable {
		t.Errorf("activity_resolution = %v, want unavailable even with no timestamp", got)
	}
}

// Review Focus 5. A pass where no adapter ran builds its Observation in the
// monitor itself, so nothing upstream supplies a resolution.
func TestAPassWithNoObserverStillCarriesTheResolution(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{err: errors.New("no observer")})

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := evidenceOf(t, st, "worker-1")["activity_resolution"]; got != policy.ResolutionUnavailable {
		t.Errorf("activity_resolution = %v, want unavailable", got)
	}
}

func TestEvidenceReachesTheEventRecord(t *testing.T) {
	s := sighting(policy.StatusWaitingInput, "session-a", t0)
	s.InputRequestVisibility = policy.VisibilityObserved
	s.InputRequestKind = policy.RequestKindQuestion
	s.InputRequestDetail = "Which approach do you want?"
	s.Evidence = policy.EvidenceTranscript
	m, st := newMonitor(t, &fakeObserver{sighting: s})

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	events, err := st.After(0, 10)
	if err != nil || len(events) == 0 {
		t.Fatalf("After: %v, %d events", err, len(events))
	}
	var got map[string]any
	if err := json.Unmarshal(events[0].Evidence, &got); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if got["evidence"] != "transcript" {
		t.Errorf("evidence = %v, want transcript", got["evidence"])
	}
	if got["input_request_kind"] != "question" {
		t.Errorf("input_request_kind = %v, want question", got["input_request_kind"])
	}
}

func TestEvidenceIsOmittedWhenThereIsNoRequest(t *testing.T) {
	m, st := newMonitor(t, &fakeObserver{sighting: sighting(policy.StatusActive, "session-a", t0)})

	if err := m.Tick(t0); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	events, _ := st.After(0, 10)
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	var got map[string]any
	if err := json.Unmarshal(events[0].Evidence, &got); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if _, present := got["evidence"]; present {
		t.Error("evidence key present with no input request")
	}
}
