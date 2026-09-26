package policy

import (
	"slices"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
)

var base = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// alerting builds a policy that surfaces everything, so a test that cares
// about one condition is not silently gated by another's default.
func alerting() config.Policy {
	return config.Policy{
		AlertOn: []string{
			config.ConditionIdle, config.ConditionInputRequested, config.ConditionExit,
			config.ConditionQuiet, config.ConditionObservationLost,
			config.ConditionNoProgress,
		},
		QuietAfter: 5 * time.Minute,
		IdleGrace:  30 * time.Second,
	}
}

func active(at time.Time) Observation {
	return Observation{
		Live: true, Status: StatusActive, StatusKnown: true,
		StatusSince: at, LastActivityAt: at, ObservedAt: at,
	}
}

func opened(d Decision, condition string) bool { return slices.Contains(d.Open, condition) }

func TestActiveSessionRaisesNothing(t *testing.T) {
	d := Evaluate(active(base), alerting(), base.Add(time.Second))
	if len(d.Open) != 0 {
		t.Errorf("Open = %v, want nothing for a session that just did something", d.Open)
	}
}

func TestObservedInputRequestOpensAnInputCondition(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince = StatusWaitingInput, base

	d := Evaluate(obs, alerting(), base.Add(time.Second))
	if !opened(d, config.ConditionInputRequested) {
		t.Errorf("Open = %v, want input_requested", d.Open)
	}
}

func TestVerifiedExitOpensAnExitCondition(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince, obs.Live = StatusExited, base, false

	d := Evaluate(obs, alerting(), base.Add(time.Second))
	if !opened(d, config.ConditionExit) {
		t.Errorf("Open = %v, want exit", d.Open)
	}
	// An exit is a verified fact, not a failure to observe.
	if opened(d, config.ConditionObservationLost) {
		t.Errorf("Open = %v, want exit without observation_lost", d.Open)
	}
}

func TestLostObservationIsDistinctFromExit(t *testing.T) {
	obs := active(base)
	obs.Live, obs.StatusKnown = false, false

	d := Evaluate(obs, alerting(), base.Add(time.Second))
	if !opened(d, config.ConditionObservationLost) {
		t.Errorf("Open = %v, want observation_lost", d.Open)
	}
	if opened(d, config.ConditionExit) {
		t.Error("losing observation was reported as an exit — that claims a fact nobody verified")
	}
}

func TestIdleOpensOnlyAfterTheGracePeriod(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince = StatusIdle, base

	if d := Evaluate(obs, alerting(), base.Add(29*time.Second)); opened(d, config.ConditionIdle) {
		t.Error("idle opened before the grace period elapsed")
	}
	if d := Evaluate(obs, alerting(), base.Add(31*time.Second)); !opened(d, config.ConditionIdle) {
		t.Error("idle did not open after the grace period elapsed")
	}
}

func TestIdleStaysClosedWhenNotEnabled(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince = StatusIdle, base

	p := alerting()
	p.AlertOn = []string{config.ConditionExit}

	if d := Evaluate(obs, p, base.Add(time.Hour)); opened(d, config.ConditionIdle) {
		t.Error("idle opened though the policy does not ask for it")
	}
}

func TestQuietOpensAfterTheConfiguredInterval(t *testing.T) {
	// Quiet applies where the provider offers no usable state: a session
	// reported as working is not silent, however old its timestamp.
	obs := active(base)
	obs.Status, obs.StatusKnown = StatusUnknown, false

	if d := Evaluate(obs, alerting(), base.Add(4*time.Minute)); opened(d, config.ConditionQuiet) {
		t.Error("quiet opened before the interval elapsed")
	}
	if d := Evaluate(obs, alerting(), base.Add(6*time.Minute)); !opened(d, config.ConditionQuiet) {
		t.Error("quiet did not open after the interval elapsed")
	}
}

// Criterion 5: a long-running tool is work in progress. Silence during it is
// explained, and calling it quiet would be reporting a stall nobody observed.
func TestRunningToolIsNotQuiet(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince = StatusRunningTool, base

	d := Evaluate(obs, alerting(), base.Add(time.Hour))
	if opened(d, config.ConditionQuiet) {
		t.Error("a session running a tool for an hour was reported quiet — the tool is the activity")
	}
}

// When a session is waiting on input, silence is already explained. A second
// incident for the same silence is the alert storm dedup exists to prevent.
func TestWaitingOnInputIsNotAlsoQuiet(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince = StatusWaitingInput, base

	d := Evaluate(obs, alerting(), base.Add(time.Hour))
	if !opened(d, config.ConditionInputRequested) {
		t.Fatalf("Open = %v, want input_requested", d.Open)
	}
	if opened(d, config.ConditionQuiet) {
		t.Error("quiet opened alongside input_requested — one silence, one incident")
	}
}

func TestIdleIsNotAlsoQuiet(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince = StatusIdle, base

	d := Evaluate(obs, alerting(), base.Add(time.Hour))
	if opened(d, config.ConditionQuiet) {
		t.Error("quiet opened alongside idle — one silence, one incident")
	}
}

// Without a usable observation there is no basis for asserting silence: the
// honest report is that observation was lost, not that nothing happened.
func TestUnobservableSessionIsNotReportedQuiet(t *testing.T) {
	obs := active(base)
	obs.Live, obs.StatusKnown = false, false

	d := Evaluate(obs, alerting(), base.Add(time.Hour))
	if opened(d, config.ConditionQuiet) {
		t.Error("quiet opened for a session birddog cannot see — silence was never established")
	}
}

// Criterion 15: an override suppresses quiet and idle, and nothing else.
func TestExpectedQuietSuppressesQuietAndIdle(t *testing.T) {
	until := base.Add(20 * time.Minute)
	p := alerting()
	p.ExpectedQuietUntil = &until

	obs := active(base)
	obs.Status, obs.StatusSince = StatusIdle, base

	d := Evaluate(obs, p, base.Add(10*time.Minute))
	if opened(d, config.ConditionQuiet) || opened(d, config.ConditionIdle) {
		t.Errorf("Open = %v, want quiet and idle suppressed during the override", d.Open)
	}
}

func TestExpectedQuietDoesNotSuppressExit(t *testing.T) {
	until := base.Add(20 * time.Minute)
	p := alerting()
	p.ExpectedQuietUntil = &until

	obs := active(base)
	obs.Status, obs.StatusSince, obs.Live = StatusExited, base, false

	d := Evaluate(obs, p, base.Add(10*time.Minute))
	if !opened(d, config.ConditionExit) {
		t.Errorf("Open = %v, want exit even while quiet is expected", d.Open)
	}
}

func TestExpectedQuietDoesNotSuppressInputRequests(t *testing.T) {
	until := base.Add(20 * time.Minute)
	p := alerting()
	p.ExpectedQuietUntil = &until

	obs := active(base)
	obs.Status, obs.StatusSince = StatusWaitingInput, base

	d := Evaluate(obs, p, base.Add(10*time.Minute))
	if !opened(d, config.ConditionInputRequested) {
		t.Errorf("Open = %v, want input_requested even while quiet is expected", d.Open)
	}
}

func TestExpectedQuietDoesNotSuppressObservationLoss(t *testing.T) {
	until := base.Add(20 * time.Minute)
	p := alerting()
	p.ExpectedQuietUntil = &until

	obs := active(base)
	obs.Live, obs.StatusKnown = false, false

	d := Evaluate(obs, p, base.Add(10*time.Minute))
	if !opened(d, config.ConditionObservationLost) {
		t.Errorf("Open = %v, want observation_lost even while quiet is expected", d.Open)
	}
}

// Criterion 15: expiry starts a fresh grace period. Firing the moment the
// override lapses would alert about exactly the silence that was authorised.
func TestOverrideExpiryStartsAFreshQuietPeriod(t *testing.T) {
	until := base.Add(20 * time.Minute)
	p := alerting() // quiet after 5 minutes
	p.ExpectedQuietUntil = &until

	obs := active(base) // last activity at base, well over an hour before
	obs.Status, obs.StatusKnown = StatusUnknown, false

	justAfter := until.Add(time.Second)
	if d := Evaluate(obs, p, justAfter); opened(d, config.ConditionQuiet) {
		t.Error("quiet opened the instant the override expired — that alerts about authorised silence")
	}
	if d := Evaluate(obs, p, until.Add(6*time.Minute)); !opened(d, config.ConditionQuiet) {
		t.Error("quiet never opened after a full fresh interval past the override")
	}
}

func TestOverrideExpiryStartsAFreshIdleGrace(t *testing.T) {
	until := base.Add(20 * time.Minute)
	p := alerting()
	p.ExpectedQuietUntil = &until

	obs := active(base)
	obs.Status, obs.StatusSince = StatusIdle, base

	if d := Evaluate(obs, p, until.Add(time.Second)); opened(d, config.ConditionIdle) {
		t.Error("idle opened the instant the override expired, without a fresh grace period")
	}
	if d := Evaluate(obs, p, until.Add(31*time.Second)); !opened(d, config.ConditionIdle) {
		t.Error("idle never opened after a full fresh grace period past the override")
	}
}

// Resolution: conditions that no longer hold are closed, so an orchestrator
// learns the worker came back.
func TestActivityResumingClosesQuietAndIdle(t *testing.T) {
	obs := active(base.Add(time.Hour))

	d := Evaluate(obs, alerting(), base.Add(time.Hour).Add(time.Second))
	for _, c := range []string{config.ConditionQuiet, config.ConditionIdle} {
		if !slices.Contains(d.Close, c) {
			t.Errorf("Close = %v, want %q closed once activity resumed", d.Close, c)
		}
	}
}

func TestResolvedInputRequestIsClosed(t *testing.T) {
	obs := active(base) // no longer waiting

	d := Evaluate(obs, alerting(), base.Add(time.Second))
	if !slices.Contains(d.Close, config.ConditionInputRequested) {
		t.Errorf("Close = %v, want input_requested closed", d.Close)
	}
}

// A condition the policy does not ask about is neither opened nor closed:
// birddog must not resolve incidents it was never asked to raise.
func TestDisabledConditionsAreNeitherOpenedNorClosed(t *testing.T) {
	p := alerting()
	p.AlertOn = []string{config.ConditionExit}

	d := Evaluate(active(base), p, base.Add(time.Hour))
	for _, c := range append(append([]string{}, d.Open...), d.Close...) {
		if c != config.ConditionExit {
			t.Errorf("condition %q acted on though the policy only asks for exit", c)
		}
	}
}

// Regaining observation closes the loss, and does not invent what was missed.
func TestRegainedObservationClosesObservationLost(t *testing.T) {
	d := Evaluate(active(base), alerting(), base.Add(time.Second))
	if !slices.Contains(d.Close, config.ConditionObservationLost) {
		t.Errorf("Close = %v, want observation_lost closed", d.Close)
	}
}

// An exited session is gone: its silence is explained by the exit, and there
// is nothing left to call quiet or idle.
func TestExitedSessionRaisesNeitherQuietNorIdle(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince, obs.Live = StatusExited, base, false

	d := Evaluate(obs, alerting(), base.Add(time.Hour))
	if opened(d, config.ConditionQuiet) || opened(d, config.ConditionIdle) {
		t.Errorf("Open = %v, want only exit for a session that ended", d.Open)
	}
}

// Contact established but the status not recognisable is a different thing
// from having lost the session. The handoff asks these be distinguished: a
// silent but healthy connection is not a broken one.
func TestContactWithAnUnrecognisedStatusIsNotObservationLost(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusKnown = StatusUnknown, false // seen, but not interpretable

	d := Evaluate(obs, alerting(), base.Add(time.Second))
	if opened(d, config.ConditionObservationLost) {
		t.Error("observation_lost opened while the session was reachable — that reports a broken connection which is not broken")
	}
}

// Silence is still reportable when contact holds: nothing was observed to
// happen, and that much is honest whatever the status meant.
func TestUnrecognisedStatusStillAllowsQuiet(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusKnown = StatusUnknown, false

	d := Evaluate(obs, alerting(), base.Add(6*time.Minute))
	if !opened(d, config.ConditionQuiet) {
		t.Error("quiet did not open for a reachable session with no activity — the silence was observed")
	}
}

// But a status nobody could interpret must never be read as idle or as a
// request for input. Those are claims about what the session is doing.
func TestUnrecognisedStatusIsNeverIdleOrInputRequested(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusKnown = StatusUnknown, false

	d := Evaluate(obs, alerting(), base.Add(time.Hour))
	if opened(d, config.ConditionIdle) || opened(d, config.ConditionInputRequested) {
		t.Errorf("Open = %v, want no claim about what an uninterpretable status means", d.Open)
	}
}

// Losing contact entirely is still observation loss, and still not quiet.
func TestLostContactIsObservationLostNotQuiet(t *testing.T) {
	obs := active(base)
	obs.Live, obs.StatusKnown = false, false

	d := Evaluate(obs, alerting(), base.Add(time.Hour))
	if !opened(d, config.ConditionObservationLost) {
		t.Errorf("Open = %v, want observation_lost", d.Open)
	}
	if opened(d, config.ConditionQuiet) {
		t.Error("quiet opened for a session with no contact — silence was never established")
	}
}

// A provider reporting the session as actively working is evidence of work.
// Claude Code stamps its status only when the status changes, so a session
// busy for an hour carries an hour-old timestamp — and calling that quiet
// reports a stall that was never observed. Same error as the long-running
// tool in criterion 5, reached by a different route.
func TestActivelyWorkingSessionIsNotQuietHoweverOldItsTimestampIs(t *testing.T) {
	obs := active(base) // last status change an hour before
	obs.Status, obs.StatusSince = StatusActive, base

	d := Evaluate(obs, alerting(), base.Add(time.Hour))
	if opened(d, config.ConditionQuiet) {
		t.Error("a session the provider reports as working was called quiet")
	}
}

// Quiet remains meaningful where the provider says nothing useful: contact
// holds, no state is readable, and nothing has been observed to happen.
func TestQuietStillAppliesWhenNoStateIsReadable(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusKnown = StatusUnknown, false

	d := Evaluate(obs, alerting(), base.Add(6*time.Minute))
	if !opened(d, config.ConditionQuiet) {
		t.Error("quiet did not open where nothing at all could be read about a reachable session")
	}
}

// Criterion 10: a laptop closed overnight wakes to find every target silent
// for eight hours. Thresholds restart from the wake rather than from activity
// that predates the sleep.
func TestThresholdFloorDelaysQuietAfterAWake(t *testing.T) {
	obs := active(base) // last seen doing anything at base
	obs.Status, obs.StatusKnown = StatusUnknown, false

	wake := base.Add(8 * time.Hour)
	obs.ThresholdFloor = wake

	if d := Evaluate(obs, alerting(), wake.Add(time.Second)); opened(d, config.ConditionQuiet) {
		t.Error("quiet opened immediately on waking — that alerts about hours nobody was watching")
	}
	if d := Evaluate(obs, alerting(), wake.Add(6*time.Minute)); !opened(d, config.ConditionQuiet) {
		t.Error("quiet never opened after a full fresh interval past the wake")
	}
}

func TestThresholdFloorDelaysIdleAfterAWake(t *testing.T) {
	obs := active(base)
	obs.Status, obs.StatusSince = StatusIdle, base

	wake := base.Add(8 * time.Hour)
	obs.ThresholdFloor = wake

	if d := Evaluate(obs, alerting(), wake.Add(time.Second)); opened(d, config.ConditionIdle) {
		t.Error("idle opened immediately on waking, without a fresh grace period")
	}
	if d := Evaluate(obs, alerting(), wake.Add(31*time.Second)); !opened(d, config.ConditionIdle) {
		t.Error("idle never opened after a full fresh grace period past the wake")
	}
}

// A wake must not suppress the conditions that are facts rather than
// durations: an exit observed after waking is still an exit.
func TestThresholdFloorDoesNotSuppressExitOrInput(t *testing.T) {
	wake := base.Add(8 * time.Hour)

	exited := active(base)
	exited.Status, exited.StatusSince, exited.Live = StatusExited, base, false
	exited.ThresholdFloor = wake
	if d := Evaluate(exited, alerting(), wake.Add(time.Second)); !opened(d, config.ConditionExit) {
		t.Error("exit was suppressed by the wake floor")
	}

	waiting := active(base)
	waiting.Status, waiting.StatusSince = StatusWaitingInput, base
	waiting.ThresholdFloor = wake
	if d := Evaluate(waiting, alerting(), wake.Add(time.Second)); !opened(d, config.ConditionInputRequested) {
		t.Error("input_requested was suppressed by the wake floor")
	}
}

// A session the provider reports as working explains its own silence, which is
// why quiet is suppressed for it — a provider need not restamp a status that
// has not changed, so a legitimately busy session carries an old timestamp and
// reading that as silence would report a stall nobody observed.
//
// The cost is that a session wedged in that state raises nothing, ever. It is
// the one state in which birddog never speaks. no_progress is the answer, at a
// horizon far above the quiet threshold, so the reasoning above survives.
func TestNoProgressOpensWhenWorkIsAssertedButNothingHappens(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = 12 * time.Hour

	d := Evaluate(active(base), p, base.Add(13*time.Hour))

	if !opened(d, config.ConditionNoProgress) {
		t.Errorf("Open = %v, want no_progress after a working session went a long time with no activity", d.Open)
	}
}

// The property that made the suppression right in the first place: a session
// genuinely working, whose status timestamp is merely old, must not trip this.
func TestNoProgressStaysClosedWithinTheHorizon(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = 12 * time.Hour

	d := Evaluate(active(base), p, base.Add(2*time.Hour))

	if opened(d, config.ConditionNoProgress) {
		t.Error("no_progress fired two hours into work, which is the false stall the suppression exists to prevent")
	}
}

// Without a configured horizon there is no defensible default, so the
// condition does nothing rather than guessing one.
func TestNoProgressDoesNothingWithoutAConfiguredHorizon(t *testing.T) {
	p := alerting()

	d := Evaluate(active(base), p, base.Add(100*24*time.Hour))

	if opened(d, config.ConditionNoProgress) {
		t.Error("no_progress fired with no horizon configured")
	}
}

// It is about work asserted and not done. A session that says it is idle is
// already covered by idle, and one nobody can see by observation_lost.
func TestNoProgressAppliesOnlyWhileWorkIsAsserted(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = time.Hour

	for _, status := range []string{StatusIdle, StatusWaitingInput, StatusExited} {
		obs := active(base)
		obs.Status = status
		if d := Evaluate(obs, p, base.Add(2*time.Hour)); opened(d, config.ConditionNoProgress) {
			t.Errorf("no_progress opened for %q, which has its own condition", status)
		}
	}
}

// A running tool is asserted work too, and is exactly the case worth catching:
// a tool that never returns looks identical to one still going.
func TestNoProgressCoversARunningTool(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = time.Hour

	obs := active(base)
	obs.Status = StatusRunningTool
	if d := Evaluate(obs, p, base.Add(2*time.Hour)); !opened(d, config.ConditionNoProgress) {
		t.Errorf("Open = %v, want no_progress for a tool that has run far past the horizon", d.Open)
	}
}

// An unreadable status is not asserted work. birddog does not know what that
// session is doing, and quiet already covers silence it cannot explain.
func TestNoProgressNeedsAReadableAssertionOfWork(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = time.Hour

	obs := active(base)
	obs.StatusKnown = false
	if d := Evaluate(obs, p, base.Add(2*time.Hour)); opened(d, config.ConditionNoProgress) {
		t.Error("no_progress opened for a session whose status birddog cannot read")
	}
}

// Authorised silence is authorised: an operator who said to expect quiet meant
// it, and no_progress is a claim about silence like the others.
func TestExpectedQuietSuppressesNoProgress(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = time.Hour
	until := base.Add(6 * time.Hour)
	p.ExpectedQuietUntil = &until

	if d := Evaluate(active(base), p, base.Add(3*time.Hour)); opened(d, config.ConditionNoProgress) {
		t.Error("no_progress fired during an authorised quiet period")
	}
}

// And a wake must not make a laptop closed overnight look like a wedged
// session, for the same reason it must not make one look quiet.
func TestThresholdFloorDelaysNoProgressAfterAWake(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = time.Hour

	wake := base.Add(8 * time.Hour)
	obs := active(base)
	obs.ThresholdFloor = wake
	if d := Evaluate(obs, p, wake.Add(time.Minute)); opened(d, config.ConditionNoProgress) {
		t.Error("no_progress fired immediately after a wake, about hours nobody was watching")
	}
}

// The policy carries the resolution and must never branch on it. If a condition
// ever starts reading it, this test is the thing that should have to change
// first — deliberately, and with the spec's D-1 reopened.
func TestActivityResolutionDoesNotChangeAnyDecision(t *testing.T) {
	p := alerting()
	// Long enough past the activity timestamp that no_progress fires, so the
	// condition that actually reads LastActivityAt is exercised rather than
	// sitting inert.
	p.NoProgressAfter = time.Hour
	now := base.Add(3 * time.Hour)
	unqualified := active(base)

	want := Evaluate(unqualified, p, now)
	for _, resolution := range []string{
		ResolutionActivity, ResolutionTransition, ResolutionUnavailable, "",
	} {
		obs := unqualified
		obs.ActivityResolution = resolution
		got := Evaluate(obs, p, now)
		if !slices.Equal(got.Open, want.Open) || !slices.Equal(got.Close, want.Close) {
			t.Errorf("resolution %q changed the decision: open=%v close=%v, want open=%v close=%v",
				resolution, got.Open, got.Close, want.Open, want.Close)
		}
	}
	if !slices.Contains(want.Open, config.ConditionNoProgress) {
		t.Fatal("no_progress did not fire, so this test is not exercising the clock it guards")
	}
}

// A duration measured from nothing is not a duration. With no activity
// timestamp there is no moment to measure silence from, and the arithmetic would
// otherwise run from the zero time and open the condition on the very first
// evaluation, whatever threshold the operator set.
func TestNoProgressNeedsAnActivityTimestampToMeasureFrom(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = time.Hour
	obs := Observation{
		Live: true, Status: StatusRunningTool, StatusKnown: true,
		ActivityResolution: ResolutionUnavailable,
	}

	d := Evaluate(obs, p, base)
	if slices.Contains(d.Open, config.ConditionNoProgress) {
		t.Error("no_progress opened with no activity timestamp: the threshold was " +
			"measured from the zero time, so any threshold fires immediately")
	}
	if !slices.Contains(d.Close, config.ConditionNoProgress) {
		t.Errorf("no_progress was neither opened nor closed; close=%v", d.Close)
	}
}

// The guard above must not disarm the condition where it has something to
// measure: a real timestamp older than the threshold still opens it.
func TestNoProgressStillOpensOnARealStaleTimestamp(t *testing.T) {
	p := alerting()
	p.NoProgressAfter = time.Hour
	obs := Observation{
		Live: true, Status: StatusRunningTool, StatusKnown: true,
		LastActivityAt:     base,
		ActivityResolution: ResolutionActivity,
	}

	d := Evaluate(obs, p, base.Add(3*time.Hour))
	if !slices.Contains(d.Open, config.ConditionNoProgress) {
		t.Errorf("no_progress did not open on a three-hour-stale timestamp; open=%v", d.Open)
	}
}
