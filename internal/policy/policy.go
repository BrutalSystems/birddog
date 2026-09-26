// Package policy turns observations into attention conditions.
//
// It is deterministic and has no clock of its own: the caller passes the time,
// so every threshold is testable exactly. It reads no worker output and makes
// no judgement about whether work is going well — an idle session is reported
// as observed idle, never as stopped early.
package policy

import (
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
)

// Runtime states birddog can observe. Absence of a state is `unknown`, which
// is a report rather than a default.
const (
	StatusActive       = "active"
	StatusIdle         = "idle"
	StatusWaitingInput = "waiting_input"
	StatusRunningTool  = "running_tool"
	StatusExited       = "exited"
	StatusUnknown      = "unknown"
)

// Input-request visibility. The distinction is the whole point: "looked for
// and absent" and "no way to look" are different answers, and collapsing them
// lets silence pass for evidence that a worker is unblocked.
const (
	VisibilityObserved    = "observed"
	VisibilityNotObserved = "not_observed"
	VisibilityUnavailable = "unavailable"
)

// Evidence says what birddog read to learn of an input request. A consumer
// deciding how far to trust InputRequestKind needs to know whether the answer
// came from the harness's own record or from birddog reading the conversation.
const (
	// EvidenceRegistry: the provider's own session record said so.
	EvidenceRegistry = "registry"

	// EvidenceHook: birddog's lifecycle hooks reported it.
	EvidenceHook = "hook"

	// EvidenceTranscript: birddog read the session transcript. The strongest
	// form is exact and the weaker one is inferred; InputRequestKind is what
	// tells them apart.
	EvidenceTranscript = "transcript"
)

// Kinds of input request birddog names itself. Provider vocabularies are
// carried verbatim; these two are birddog's own, because birddog is what
// distinguished them.
const (
	// RequestKindQuestion: a question the session is provably blocked on.
	RequestKindQuestion = "question"

	// RequestKindQuestionInferred: a question inferred from the shape of the
	// conversation. Reported separately because it can be wrong.
	RequestKindQuestionInferred = "question_inferred"
)

// Activity resolution says what the activity timestamp beside it actually is,
// so a consumer can tell how stale it may be while the session is healthy.
//
// The value names what is *watching*, not which timestamp happened to be latest
// on this pass. Where birddog's own instrumentation is reporting, tool work
// would have moved the timestamp, so staleness is meaningful even on a pass
// whose newest value came from a transition.
const (
	// ResolutionActivity: birddog's instrumentation is reporting this session,
	// so the timestamp moves at tool-boundary resolution and staleness beyond
	// seconds means something.
	ResolutionActivity = "activity"

	// ResolutionTransition: the value is the session's own status timestamp,
	// which moves only when the state changes. Staleness up to the length of a
	// whole turn is ordinary — measured at 27 minutes on a run that ended
	// normally.
	ResolutionTransition = "transition"

	// ResolutionUnavailable: nothing is watching this session's activity, so
	// there is no signal to qualify. A different answer from a coarse signal,
	// the same way "no way to look" differs from "looked and saw nothing".
	//
	// About the signal, not about the timestamp being zero: the two come apart
	// for the process adapter, which watches named paths and reports activity
	// even for one that does not exist yet and so carries no timestamp.
	ResolutionUnavailable = "unavailable"
)

// Reasons why an observation could not produce a current, readable answer.
//
// birddog already reports that it could not see. The reason says which of
// several different situations that was, because they call for different
// responses: a provider that structurally cannot report this is not a helper
// process that died, and neither is a file that became unreadable.
//
// Deliberately small, provider-independent, and absent rather than guessed. A
// reason is a contract, and a wrong one is worse than none.
const (
	// ReasonContactLost: the session could not be reached this pass. It may
	// well still be running — this is the absence of evidence, not evidence
	// of absence.
	ReasonContactLost = "contact_lost"

	// ReasonSourceUnreadable: what birddog reads to observe this target could
	// not be read. An operator fault rather than anything about the session.
	ReasonSourceUnreadable = "source_unreadable"

	// ReasonStatusUnrecognised: the session was reached and reported a value
	// whose meaning has not been established, so it is not interpreted.
	// Often the first sign of a new provider version.
	ReasonStatusUnrecognised = "status_unrecognised"

	// ReasonProviderLimited: this cannot be observed from outside for this
	// provider, at any version. Asking again will not help.
	ReasonProviderLimited = "provider_limited"
)

// How well birddog can observe one capability of one provider, as a value a
// program can branch on rather than a sentence it must parse.
//
// This is the static half of the pair. A reason says why a single observation
// came back indeterminate; a support value says whether it could ever have
// been otherwise. A consumer needs both, and neither is much use alone.
//
// These describe what the installation supports, not what a particular
// session happens to do. Whether this machine has the instrumentation in
// place is reported separately, by the Instrument values below.
const (
	// SupportSupported: observable as installed, with nothing further to do.
	SupportSupported = "supported"

	// SupportRequiresSetup: observable once birddog's own instrumentation is
	// in place — hooks, or the opencode plugin. A consumer seeing this knows
	// there is an action that makes the capability true, which is what
	// separates it from a limit.
	SupportRequiresSetup = "requires_setup"

	// SupportPartial: the field bundles more than one thing and the answers
	// differ. Read the sentence beside it; the parts cannot be told apart
	// from the value alone.
	SupportPartial = "partial"

	// SupportProviderLimited: not observable from outside for this provider,
	// at any version. Asking again will not help, and no setup changes it.
	// The same meaning as ReasonProviderLimited, stated about the provider
	// rather than about one observation.
	SupportProviderLimited = "provider_limited"
)

// Instrumentation values say whether birddog's own observation machinery is in
// place on THIS machine.
//
// A support value says a capability could be made true; these say whether it
// has been. requires_setup with the setup in place and requires_setup with
// nothing installed are the same value on the capability and different
// situations for a consumer, and only these tell them apart.
//
// Deliberately a separate vocabulary from Support above. One describes what a
// provider can ever expose, the other what this installation has done about
// it; folding them together would put two axes in one enum, which is the flaw
// `partial` already demonstrates.
const (
	// InstrumentNotInstalled: nothing is installed. Not a fault — running
	// without birddog's instrumentation is a legitimate choice, and this
	// value never carries a problem.
	InstrumentNotInstalled = "not_installed"

	// InstrumentInstalled: installed, and nothing says it cannot work.
	InstrumentInstalled = "installed"

	// InstrumentIncomplete: installed for some of what it covers and not the
	// rest. Worse than absent, because it looks like coverage.
	InstrumentIncomplete = "incomplete"

	// InstrumentBroken: installed and cannot work — a handler that is not
	// there, or a plugin whose last attempt to publish failed. This is
	// birddog's own fault rather than a provider's limit, and it is the one
	// instrumentation value that carries a problem.
	InstrumentBroken = "broken"

	// InstrumentUnknown: it cannot be established from here. Reported rather
	// than resolved to not_installed, because an absence of evidence is not
	// evidence of absence and birddog does not report it as one.
	InstrumentUnknown = "unknown"
)

// Observation is what the policy may consider, plus what birddog reports
// alongside it.
//
// InputRequestVisibility and ActivityResolution are carried here and read by no
// condition: they qualify what was seen rather than deciding anything. Evaluate
// must not branch on either.
type Observation struct {
	// Live reports whether the session could be observed at all.
	Live bool

	// Status is the observed runtime state; StatusKnown says whether it was
	// observed rather than merely absent.
	Status      string
	StatusKnown bool

	// StatusSince is when the session entered this state.
	StatusSince time.Time

	// LastActivityAt is the last session-scoped action or output. Adapter
	// heartbeats must never advance it.
	LastActivityAt time.Time

	// ObservedAt is when this observation was taken.
	ObservedAt time.Time

	// InputRequestVisibility says whether a permission or input wait could be
	// observed for this session at all — never whether one exists.
	InputRequestVisibility string

	// ActivityResolution says what LastActivityAt is: observed activity, a
	// status transition, or nothing watching at all. Reported rather than acted
	// on, so a consumer can interpret staleness instead of guessing at it.
	//
	// It describes what is watching, so it is stable while that holds — and it
	// is set independently of whether the timestamp has a value yet.
	ActivityResolution string

	// InputRequestKind is what the provider called the kind of request —
	// "bash", "edit", "permission prompt". Carried verbatim: normalising
	// several providers' vocabularies into one would assert an equivalence
	// between them that nobody established.
	InputRequestKind string

	// InputRequestDetail is the subject of an observed request, where the
	// provider says. "rm test.tst" is worth far more to an orchestrator than
	// knowing only that approval is outstanding.
	InputRequestDetail string

	// Evidence says how an observed input request was learned: registry,
	// hook or transcript. Empty when there is no request to describe.
	Evidence string

	// Reason says why this observation is not a current, readable answer.
	// Empty when it is one: a reason is for the indeterminate case, not
	// decoration.
	Reason string

	// ThresholdFloor is the earliest moment durations may be measured from.
	// The monitor sets it after the machine wakes, so quiet and idle restart
	// at the wake instead of firing about hours nobody was watching.
	ThresholdFloor time.Time
}

// Decision is the set of conditions that should now be open, and those that
// should be resolved. A condition the policy does not ask about appears in
// neither: birddog does not resolve incidents it was never asked to raise.
type Decision struct {
	Open  []string
	Close []string
}

// Evaluate decides which attention conditions hold at time now.
func Evaluate(obs Observation, p config.Policy, now time.Time) Decision {
	var d Decision

	// Silence is suppressed, not unobserved: an override changes what is
	// reported, never what is recorded.
	quietSuppressed := suppressedBy(p, now)

	// After an override lapses, thresholds are measured from the expiry, not
	// from the last activity — otherwise the first evaluation afterwards
	// alerts about precisely the silence that was authorised.
	// Thresholds never reach back past a floor: whichever of a lapsed
	// override or a wake is later.
	from := laterOf(overrideFloor(p, now), obs.ThresholdFloor)

	exited := obs.Status == StatusExited && obs.StatusKnown

	// Two different things, deliberately kept apart. Contact is whether the
	// session could be reached at all; a readable status is whether what it
	// reported means anything to us. A reachable session whose status we
	// cannot interpret is not a broken connection, and saying so would send
	// the orchestrator looking for a fault that is not there.
	inContact := obs.Live
	readable := obs.Live && obs.StatusKnown

	// Exit is a verified fact and explains everything that follows from it.
	set(&d, p, config.ConditionExit, exited)

	// Losing observation is not an exit. It is the absence of evidence, and
	// is reported as such.
	set(&d, p, config.ConditionObservationLost, !exited && !inContact)

	// Claims about what a session is doing require a status we can read.
	waiting := readable && obs.Status == StatusWaitingInput
	set(&d, p, config.ConditionInputRequested, waiting)

	idle := readable && obs.Status == StatusIdle &&
		!now.Before(laterOf(obs.StatusSince, from).Add(p.IdleGrace))
	set(&d, p, config.ConditionIdle, idle && !quietSuppressed)

	// Quiet means no relevant activity was observed — not that the worker
	// stalled. It applies only when nothing else already explains the
	// silence.
	//
	// A readable status usually is that explanation. A tool still running is
	// activity; a session reported as working is activity; and a session
	// waiting or idle has its own incident carrying the duration. What is
	// left — and what quiet is actually for — is a reachable session whose
	// state says nothing useful and where nothing has been seen to happen.
	//
	// This matters because a provider need not restamp a status that has not
	// changed: Claude Code's timestamp moves only on a transition, so a
	// session busy for an hour carries an hour-old timestamp. Reading that as
	// silence would report a stall nobody observed.
	// Silence needs contact to have been established, but not a readable
	// status: "nothing was observed to happen" is honest either way.
	working := readable && (obs.Status == StatusActive || obs.Status == StatusRunningTool)
	silenceExplained := waiting || idle || exited || working

	// A session asserting work explains its own silence, which is why quiet
	// is suppressed above — and why, without this, a session wedged while
	// reporting work raises nothing at all. It is the one state birddog never
	// speaks about.
	//
	// So the same activity clock is consulted, at a horizon the operator sets
	// and far above the quiet threshold. Nothing is assumed when they set
	// none: a wrong threshold here reintroduces exactly the false stalls the
	// suppression prevents.
	//
	// An activity timestamp is required, not merely used. Without one there is no
	// moment to measure silence from, and laterOf would fall back to the zero
	// time — so every threshold, however large, would be exceeded on the first
	// evaluation. That is reachable: a process target watching no files reports
	// running_tool whenever it has a descendant and carries no timestamp at all.
	// A duration measured from nothing is not a duration.
	noProgress := working && p.NoProgressAfter > 0 &&
		!obs.LastActivityAt.IsZero() &&
		now.After(laterOf(obs.LastActivityAt, from).Add(p.NoProgressAfter))
	set(&d, p, config.ConditionNoProgress, noProgress && !quietSuppressed)
	quiet := inContact && !exited && !silenceExplained &&
		now.After(laterOf(obs.LastActivityAt, from).Add(p.QuietAfter))
	set(&d, p, config.ConditionQuiet, quiet && !quietSuppressed)

	return d
}

// set records a condition as open or closed, but only when the target's policy
// asks about it.
func set(d *Decision, p config.Policy, condition string, holds bool) {
	if !p.AlertsOn(condition) {
		return
	}
	if holds {
		d.Open = append(d.Open, condition)
		return
	}
	d.Close = append(d.Close, condition)
}

// suppressedBy reports whether an expected-quiet override is currently active.
func suppressedBy(p config.Policy, now time.Time) bool {
	return p.ExpectedQuietUntil != nil && now.Before(*p.ExpectedQuietUntil)
}

// overrideFloor returns the moment a lapsed override ended, so thresholds
// restart from there rather than from activity that predates it.
func overrideFloor(p config.Policy, now time.Time) time.Time {
	if p.ExpectedQuietUntil == nil || now.Before(*p.ExpectedQuietUntil) {
		return time.Time{}
	}
	return *p.ExpectedQuietUntil
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// MaxDetail bounds what an observed request's subject may contribute to the
// durable record. It matches the bound the opencode plugin applies when it
// writes one.
//
// It is the one path by which unbounded text from a watched session reaches
// the log at all. A permission request's subject is a command line, a path or
// an edit target, and a command line can carry a credential; a question read
// from a transcript is free-form prose. birddog records evidence, not
// payloads.
//
// It lives here rather than beside one adapter because every adapter that
// reports a subject must apply it, and internal/observe is not importable
// from all of them.
const MaxDetail = 200

// BoundDetail limits a request subject to MaxDetail runes, marking any value
// it had to cut.
//
// Runes rather than bytes: cutting mid-character would put an invalid rune in
// the durable record. Over-long input is a malformed record rather than a
// reason to drop the observation, so the value is truncated and reported.
func BoundDetail(s string) string {
	r := []rune(s)
	if len(r) <= MaxDetail {
		return s
	}
	return string(r[:MaxDetail]) + "…"
}
