// Package monitor is birddog's observation loop.
//
// One pass observes every target, records what was seen, asks the policy which
// attention conditions hold, and opens or resolves incidents accordingly. It
// sends nothing to any watched session and controls none of them.
package monitor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/notify"
	"github.com/BrutalSystems/birddog/internal/platform/wake"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/store"
)

// Sighting is what an adapter saw, plus the identity of the session that
// produced it.
type Sighting struct {
	policy.Observation

	// SessionIdentity distinguishes one run from its replacement. When it
	// changes, the target is a new generation — not a continuation — so
	// nothing observed of the old session can describe the new one.
	SessionIdentity string
}

// Observer reads the current state of one target. It must never resume a
// conversation, start a turn, or inject a prompt to find out.
type Observer interface {
	Observe(target config.Target) (Sighting, error)
}

// Monitor runs observation passes for one instance.
type Monitor struct {
	instanceID string

	// machine identifies the machine observations are made on, empty in local
	// mode. Supplied rather than discovered; see internal/machine.
	machine   string
	cfg       *config.Config
	store     *store.Store
	observers map[string]Observer

	// wake notices the machine having slept, so thresholds do not reach back
	// across time nobody was watching.
	wake    *wake.Detector
	elapsed func() time.Duration

	// lost records which targets the last pass learned nothing about, so
	// regaining sight of one can restart its thresholds. Silence birddog did
	// not witness is not a duration it may measure.
	lost map[string]bool

	// targetFloor is the earliest moment thresholds may reach back to for one
	// target — set when its observation is regained, the same way the
	// instance-wide floor is set after a wake.
	targetFloor map[string]time.Time

	// notifier delivers alerts to the orchestrator. Defaults to None, so an
	// instance with no transport configured still records everything.
	notifier    notify.Notifier
	maxAttempts int

	// configRev stamps events with the watch list they were observed under.
	configRev int64

	// floor is the moment the last detected wake happened. Passes after it
	// measure durations from there rather than from before the sleep.
	floor time.Time
}

// wakeThreshold is how far the wall and monotonic clocks may diverge before a
// pass counts as following a sleep. Comfortably above scheduling noise, well
// below any sleep worth noticing.
const wakeThreshold = time.Minute

// processStart anchors the monotonic elapsed reading.
var processStart = time.Now()

// New builds a monitor over a watch list and the observers that can serve it.
func New(instanceID string, cfg *config.Config, st *store.Store, observers map[string]Observer) *Monitor {
	return &Monitor{
		instanceID:  instanceID,
		cfg:         cfg,
		store:       st,
		observers:   observers,
		wake:        wake.New(wakeThreshold),
		elapsed:     func() time.Duration { return time.Since(processStart) },
		notifier:    notify.None{},
		maxAttempts: defaultDeliveryAttempts,
		lost:        map[string]bool{},
		targetFloor: map[string]time.Time{},
	}
}

// defaultDeliveryAttempts bounds retries of a definite failure. An orchestrator
// that has gone away must not be hammered.
const defaultDeliveryAttempts = 3

// SetConfigRevision records which watch list observations are made under, so a
// delayed one can be told apart from one made under the current rules.
func (m *Monitor) SetConfigRevision(rev int64) { m.configRev = rev }

// SetMachine records which machine observations are made on, so a cursor from
// this instance is comparable with one from another host.
func (m *Monitor) SetMachine(id string) { m.machine = id }

// SetNotifier chooses where alerts are delivered.
func (m *Monitor) SetNotifier(n notify.Notifier) { m.notifier = n }

// SetElapsed replaces the monotonic elapsed reading, so tests can drive a
// sleep without one.
func (m *Monitor) SetElapsed(f func() time.Duration) { m.elapsed = f }

// Tick runs one observation pass.
//
// An error from one target does not end the pass: a provider that has gone
// away is an observation to record, not a reason to stop watching everything
// else. Tick returns an error only when the store itself fails.
func (m *Monitor) Tick(now time.Time) error {
	// Wall-clock time passing that monotonic time did not means the machine
	// slept. Thresholds then restart from the wake, or a laptop closed
	// overnight reports every target silent for eight hours.
	if jump := m.wake.Check(now, m.elapsed()); jump.Jumped {
		m.floor = jump.Floor
	}

	// Sighted first, decided second. A failure that hit every target at once
	// says something about birddog's own view, and that has to be known
	// before any conclusion is drawn from an individual target.
	sightings := make([]Sighting, len(m.cfg.Targets))
	errs := make([]error, len(m.cfg.Targets))
	learned := 0
	for i, target := range m.cfg.Targets {
		sightings[i], errs[i] = m.sight(target)
		if observedSomething(sightings[i]) {
			learned++
		}
	}

	// Every target unobservable in the same pass is not evidence about every
	// session. It is the shape a dead adapter child, an unreadable registry
	// directory, or birddog itself being starved produces — and the sessions
	// are most likely fine and simply out of view.
	//
	// birddog cannot tell which, so it claims neither. What it can do is stop
	// measuring durations across a period it was not really watching, exactly
	// as it does after a sleep, and say on the record that the loss was
	// correlated so an operator sees one fault rather than inferring N.
	correlated := len(m.cfg.Targets) > 1 && learned == 0
	if correlated {
		m.floor = now
	}

	for i, target := range m.cfg.Targets {
		if err := m.observeTarget(target, sightings[i], errs[i], correlated, now); err != nil {
			return err
		}
	}
	return nil
}

// observedSomething reports whether a pass learned anything about a target.
//
// A verified exit is an observation, not a gap: the session is known to be
// gone. Only a sighting that is neither live nor readable means birddog came
// away with nothing.
func observedSomething(s Sighting) bool { return s.Live || s.StatusKnown }

func (m *Monitor) observeTarget(target config.Target, sighting Sighting, observeErr error, correlated bool, now time.Time) error {
	prior, hadPrior, err := m.store.TargetState(target.ID)
	if err != nil {
		return err
	}
	generation := generationFor(prior, hadPrior, sighting.SessionIdentity)

	// Regaining sight of a target is the same situation as waking: the
	// silence in between was not observed, so it cannot be measured. Without
	// this, a target whose adapter was broken for twenty minutes reports
	// quiet the moment it comes back, about a gap birddog could not see.
	if observedSomething(sighting) {
		if m.lost[target.ID] {
			m.targetFloor[target.ID] = now
			delete(m.lost, target.ID)
		}
	} else {
		m.lost[target.ID] = true
	}

	sighting.ThresholdFloor = laterOf(m.floor, m.targetFloor[target.ID])
	decision := policy.Evaluate(sighting.Observation, target.Policy, now)

	// A verified exit is terminal: the session will never report anything
	// again, so observing it a second time asserts nothing new. Nothing is
	// written for a repeat — not the event, and not the incident touch that
	// evaluating the policy would perform. One measured 22-hour case cost
	// 37,305 events and as many incident writes saying the same thing.
	//
	// A repeat is not on its own proof that the pass would change nothing, so
	// the incident state decides. An intervening pass that observed nothing
	// resolves the exit incident and opens observation_lost; every later pass
	// then restates the exit, and suppressing on the repeat alone would leave
	// the exit incident resolved forever. `OpenIncidents` filters on
	// resolved_ms, so `status` would stop listing it and `ack` — which takes
	// only an incident id — would have nothing to name: a terminal outcome that
	// cannot be collected, which is the loss this whole design exists to
	// prevent.
	//
	// Consulting the store rather than remembering in process keeps this
	// correct across a daemon restart, and a read is not a write, so D-1 holds
	// literally: last_seen_at still freezes.
	if isRepeatTerminal(prior, hadPrior, sighting, generation) {
		settled, err := m.settled(target, decision, generation)
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
	}

	if err := m.record(target, sighting, observeErr, correlated, generation, now); err != nil {
		return err
	}

	return m.applyDecision(target, decision, generation, now)
}

// settled reports whether applying d would change nothing: the target's open
// incidents are already exactly what d would produce, and none of them is
// awaiting a delivery attempt.
//
// It mirrors applyDecision condition for condition, deliberately. Anything it
// misses is a pass suppressed while it still had work to do, and the two writes
// that matter — opening an incident and resolving one — are both permanent in
// their effect on what `status` reports and what `ack` can name.
//
// The delivery half is load-bearing on its own: applyDecision is the only
// driver of a retry, so a failed attempt on the exit alert would never get its
// remaining attempts if a repeat pass returned early regardless.
func (m *Monitor) settled(target config.Target, d policy.Decision, generation int64) (bool, error) {
	open, err := m.store.OpenIncidents(target.ID)
	if err != nil {
		return false, err
	}

	// At most one open incident per target, condition and generation, enforced
	// by the incidents_open index, so this loses nothing.
	current := make(map[string]store.Incident, len(open))
	for _, inc := range open {
		if inc.Generation == generation {
			current[inc.Condition] = inc
		}
	}

	for _, condition := range d.Open {
		inc, ok := current[condition]
		if !ok {
			// applyDecision would open it, and an unopened condition is the
			// one thing a consumer cannot find out about later.
			return false, nil
		}
		if inc.NotifiedAt != nil {
			continue
		}
		should, err := m.store.ShouldAttemptDelivery(inc.ID, m.maxAttempts)
		if err != nil {
			return false, err
		}
		if should {
			return false, nil
		}
	}

	if len(d.Close) == 0 {
		// applyDecision does not read the open set at all in this case, so
		// nothing it would do depends on what is open.
		return true, nil
	}
	for _, inc := range open {
		// A superseded run's incident is closed out regardless of condition,
		// but only on a pass that closes something.
		if inc.Generation < generation {
			return false, nil
		}
	}
	for _, condition := range d.Close {
		if _, ok := current[condition]; ok {
			return false, nil
		}
	}
	return true, nil
}

// isRepeatTerminal reports whether this sighting restates a verified exit that
// is already the target's recorded state, for the same run.
//
// Deliberately narrow. It is not "the status is unchanged": re-observing a live
// session is evidence it is still there now, which is new information. Only a
// terminal state can be restated without saying anything, because only a
// terminal state can never change again.
//
// Reason is not compared, although D-1 lists a reason appearing among the
// things that resume recording. Every adapter sets Reason only alongside
// StatusKnown: false, which the check above already rejects, so an
// exited-with-a-new-reason sighting cannot occur today. An adapter that started
// reporting one would need this to compare Reason as well.
func isRepeatTerminal(prior store.TargetStateRecord, hadPrior bool, s Sighting, generation int64) bool {
	if !hadPrior {
		return false
	}
	if !s.StatusKnown || s.Status != policy.StatusExited {
		return false
	}
	return prior.Status == policy.StatusExited && prior.Generation == generation
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// sight asks the target's observer what it can see.
//
// A missing observer and a failing one produce the same honest answer: nothing
// was observed. Neither is reported as an exit, which would claim a fact
// nobody verified.
func (m *Monitor) sight(target config.Target) (Sighting, error) {
	observer, ok := m.observers[target.Provider]
	if !ok {
		return unobserved(), fmt.Errorf("no observer for provider %q", target.Provider)
	}
	sighting, err := observer.Observe(target)
	if err != nil {
		return unobserved(), err
	}
	return sighting, nil
}

func unobserved() Sighting {
	return Sighting{Observation: policy.Observation{
		Live: false, Status: policy.StatusUnknown, StatusKnown: false,
		// No adapter ran, so nothing was watching this session's activity.
		ActivityResolution: policy.ResolutionUnavailable,
	}}
}

// generationFor returns the run generation for a target, advancing it when the
// session behind it has been replaced.
//
// Takes the prior state rather than reading it, so one pass reads a target's
// state once and both the generation and the terminal-repeat check work from
// the same row.
func generationFor(prior store.TargetStateRecord, hadPrior bool, identity string) int64 {
	switch {
	case !hadPrior:
		return 1

	case identity == "":
		// Nothing was observed this pass, so there is no evidence the session
		// changed. Keep the generation rather than inventing a new run.
		return prior.Generation

	case prior.SessionID == "":
		// The target has been seen before but never identified — it was
		// unobservable until now. Learning its identity is not a change of
		// run, and treating it as one orphans the observation-lost incident
		// raised moments earlier: incidents resolve only within their own
		// generation, so the target would report a condition that has
		// demonstrably ended, forever.
		return prior.Generation

	case identity != prior.SessionID:
		return prior.Generation + 1

	default:
		return prior.Generation
	}
}

// record appends the observation to the event feed and, when something was
// actually observed, applies it as the target's state.
//
// A failed observation is recorded but asserts no status: overwriting the last
// known state with "unknown" would destroy the very thing the handoff asks to
// be preserved and marked stale.
func (m *Monitor) record(target config.Target, s Sighting, observeErr error, correlated bool, generation int64, now time.Time) error {
	evidence := map[string]any{
		"live":         s.Live,
		"status_known": s.StatusKnown,
	}
	if correlated {
		// Every target went unobservable in this pass. Recorded as a fact
		// about the pass, not as a conclusion about any session.
		evidence["correlated_loss"] = true
	}
	if len(target.Labels) > 0 {
		// Opaque correlation metadata: carried, never interpreted.
		evidence["labels"] = target.Labels
	}
	if observeErr != nil {
		evidence["error"] = observeErr.Error()
	}
	if !s.LastActivityAt.IsZero() {
		evidence["last_activity_at"] = s.LastActivityAt.UTC().Format(time.RFC3339)
	}
	// Written whether or not there is a timestamp. "No signal" is the answer
	// unavailable exists to give, and a consumer must be able to tell it from a
	// key a build predating this field never emitted — so the qualifier does not
	// live inside the guard above.
	resolution := s.ActivityResolution
	if resolution == "" {
		resolution = policy.ResolutionUnavailable
	}
	evidence["activity_resolution"] = resolution
	if s.InputRequestKind != "" {
		evidence["input_request_kind"] = s.InputRequestKind
	}
	if s.InputRequestDetail != "" {
		evidence["input_request"] = s.InputRequestDetail
	}
	if s.Evidence != "" {
		evidence["evidence"] = s.Evidence
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("encode evidence: %w", err)
	}

	event := store.Event{
		InstanceID: m.instanceID,
		TargetID:   target.ID,
		Type:       "observation",
		Source:     target.Provider,
		At:         now,
		SessionID:  s.SessionIdentity,
		Generation: generation,
		ConfigRev:  m.configRev,
		Machine:    m.machine,
		Evidence:   payload,
	}
	if s.StatusKnown {
		event.Status = s.Status
	}
	event.InputRequestVisibility = s.InputRequestVisibility
	event.Reason = s.Reason

	_, _, err = m.store.Record(event)
	return err
}

// deliver tells the orchestrator about an incident, once.
//
// Delivery never affects observation: a transport that is down, slow or
// uncertain costs one channel, and every alert stays in the event feed and in
// `status` regardless.
func (m *Monitor) deliver(target config.Target, inc store.Incident, now time.Time) error {
	should, err := m.store.ShouldAttemptDelivery(inc.ID, m.maxAttempts)
	if err != nil {
		return err
	}
	if !should {
		return nil
	}

	state, _, err := m.store.TargetState(target.ID)
	if err != nil {
		return err
	}

	outcome, _ := m.notifier.Deliver(notify.Alert{
		InstanceID: m.instanceID,
		TargetID:   target.ID,
		IncidentID: inc.ID,
		Condition:  inc.Condition,
		OpenedAt:   inc.OpenedAt,
		Status:     state.Status,
		Source:     state.Source,
		ObservedAt: state.ObservedAt,
		// Nothing can observe a permission request yet; saying so in the
		// alert keeps silence from reading as evidence.
		InputRequestVisibility: "unavailable",
		Labels:                 target.Labels,
	})

	if err := m.store.RecordDelivery(inc.ID, m.notifier.Name(), outcome.String(), now); err != nil {
		return err
	}
	if outcome == notify.Delivered {
		return m.store.MarkNotified(inc.ID, now)
	}
	return nil
}

func (m *Monitor) applyDecision(target config.Target, d policy.Decision, generation int64, now time.Time) error {
	for _, condition := range d.Open {
		inc, opened, err := m.store.OpenOrTouchIncident(target.ID, condition, generation, now)
		if err != nil {
			return err
		}
		// Notify when a condition opens or has not yet got through. A repeat
		// observation of an unchanged condition says nothing new.
		if opened || inc.NotifiedAt == nil {
			if err := m.deliver(target, inc, now); err != nil {
				return err
			}
		}
	}
	if len(d.Close) == 0 {
		return nil
	}

	open, err := m.store.OpenIncidents(target.ID)
	if err != nil {
		return err
	}
	for _, inc := range open {
		// An incident belonging to a run that has been superseded is closed
		// out regardless of condition. Its session is gone, so no evidence
		// about it will ever arrive, and leaving it open reports a condition
		// of a session that no longer exists.
		if inc.Generation < generation {
			if err := m.store.ResolveIncident(inc.ID, now); err != nil {
				return err
			}
			continue
		}

		for _, condition := range d.Close {
			if inc.Condition == condition && inc.Generation == generation {
				if err := m.store.ResolveIncident(inc.ID, now); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
