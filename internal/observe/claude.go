// Package observe adapts each provider's discovery to the monitor's view of a
// session.
//
// Every adapter here is read-only. None resumes a conversation, starts a turn,
// or sends anything to a session in order to find out what it is doing.
package observe

import (
	"fmt"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/hooks"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/observe/transcript"
	"github.com/BrutalSystems/birddog/internal/platform/proc"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/provider"
)

// Claude observes Claude Code sessions through the harness's session registry.
type Claude struct {
	// List reads the registry. Injected so the adapter is testable without
	// any running session.
	List func() ([]claude.Session, error)

	// SameProcess verifies a recorded process identity. Defaults to the real
	// check.
	SameProcess func(pid int, procStart string) bool

	// HookState reads what Claude Code's lifecycle hooks reported. The
	// session registry carries neither tool boundaries nor permission
	// decisions, so without hooks installed there is nothing to read and
	// birddog says so rather than inferring.
	HookState func(sessionID string) (hooks.State, error)
}

// NewClaude builds an observer over the real session registry and, where
// hooks are installed, the state they record.
func NewClaude(registryDir, hookDir string) *Claude {
	store := &hooks.Store{Dir: hookDir}
	return &Claude{
		List: func() ([]claude.Session, error) {
			return claude.List(claude.Params{RegistryDir: registryDir})
		},
		HookState: func(sessionID string) (hooks.State, error) {
			return store.Read(sessionID)
		},
	}
}

// statusMap is the part of the harness's status vocabulary whose meaning is
// established. Anything outside it is reported as unreadable rather than
// guessed, because claiming a meaning nobody verified manufactures alerts
// nobody observed.
//
// "waiting" earned its place by observation rather than by assumption. On
// Claude Code 2.1.267, a session blocked on a permission prompt reports
// "waiting" and carries a waitingFor field, and both clear when the prompt is
// answered; the transition observed was idle -> busy -> waiting. Pinned to
// that version and claimed no further. Values still seen but unexplained —
// "compacting", "requesting", "tool_use" — stay out until the same is done
// for them.
var statusMap = map[string]string{
	"busy":    policy.StatusActive,
	"idle":    policy.StatusIdle,
	"waiting": policy.StatusWaitingInput,
}

// Observe reports the current state of one Claude Code session.
func (c *Claude) Observe(target config.Target) (monitor.Sighting, error) {
	sessions, err := c.List()
	if err != nil {
		return unreadable(policy.ReasonSourceUnreadable), fmt.Errorf("read Claude Code session registry: %w", err)
	}

	for _, s := range sessions {
		if s.SessionID != target.Attachment.SessionID {
			continue
		}
		sighting := c.sighting(s)
		hookErr := c.enrich(&sighting, s)
		return sighting, hookErr
	}
	return c.absent(target)
}

// enrich adds what the hooks saw to what the registry said.
//
// The registry decides the base state; hooks supply only what it cannot
// carry — tool boundaries and permission decisions. Nothing here revives a
// session the registry says is unreachable: markers left by a session that
// has gone are as stale as everything else about it.
func (c *Claude) enrich(sighting *monitor.Sighting, s claude.Session) error {
	if c.HookState == nil || !s.Live {
		return nil
	}

	state, err := c.HookState(s.SessionID)
	if err != nil {
		// The registry observation stands; only the enrichment is lost.
		return fmt.Errorf("read hook state for session %s: %w", s.SessionID, err)
	}
	if !state.Present {
		// No hook has reported this session, so birddog has not looked.
		// Reporting "no requests" here would turn a missing installation
		// into evidence about the session.
		return nil
	}

	// Hooks were looking, and this is what they saw.
	sighting.InputRequestVisibility = policy.VisibilityNotObserved
	// Whatever the registry sourced a request to, the hooks are the better
	// witness and are not reporting one. Evidence describes a request that is
	// being reported; it is restored below if one is.
	sighting.Evidence = ""

	// Tool boundaries move the activity timestamp, so staleness is meaningful
	// at a far shorter horizon. Set from the fact that hooks are reporting at
	// all, not from whether a marker happens to be newer than the status
	// timestamp on this pass — the question a consumer is asking is how stale
	// the value can be while the session is healthy.
	sighting.ActivityResolution = policy.ResolutionActivity

	switch {
	case len(state.PendingPermissions) > 0:
		// Waiting on a human outranks everything: it is the condition an
		// orchestrator most needs, and a tool running underneath does not
		// make the session unblocked.
		sighting.Status = policy.StatusWaitingInput
		sighting.StatusKnown = true
		sighting.StatusSince = state.PendingPermissions[0].AskedAt
		sighting.InputRequestVisibility = policy.VisibilityObserved
		sighting.InputRequestDetail = boundDetail(state.PendingPermissions[0].Tool)
		sighting.Evidence = policy.EvidenceHook

	case len(state.RunningTools) > 0:
		sighting.Status = policy.StatusRunningTool
		sighting.StatusKnown = true
		sighting.StatusSince = state.RunningTools[0].StartedAt
	}

	if state.LastActivityAt.After(sighting.LastActivityAt) {
		sighting.LastActivityAt = state.LastActivityAt
	}
	return nil
}

// visibilityFor says whether a permission or input wait could be observed for
// this session — never whether one exists.
//
// The registry reports a waiting session, so a session it does not report as
// waiting is one birddog looked at and saw no request for: not_observed rather
// than unavailable. The claim is bounded by what the registry covers, which is
// what not_observed already means and what an alert says about it.
//
// A session that cannot be seen at all gets unavailable, because its last
// status is as stale as everything else about it and says nothing current.
// evidenceFor names what was read to learn of an input request, and is empty
// when there is none: an observation with no request has nothing to source.
func evidenceFor(s claude.Session) string {
	if visibilityFor(s) == policy.VisibilityObserved {
		return policy.EvidenceRegistry
	}
	return ""
}

func visibilityFor(s claude.Session) string {
	switch {
	case !s.Live:
		return policy.VisibilityUnavailable
	case s.Status == "waiting":
		return policy.VisibilityObserved
	default:
		return policy.VisibilityNotObserved
	}
}

// claudeReason says why an observation is not a current, readable answer.
func claudeReason(s claude.Session, statusKnown bool) string {
	switch {
	case !s.Live:
		return policy.ReasonContactLost
	case !statusKnown:
		return policy.ReasonStatusUnrecognised
	default:
		return ""
	}
}

func (c *Claude) sighting(s claude.Session) monitor.Sighting {
	status, known := statusMap[s.Status]
	if !known {
		status = policy.StatusUnknown
	}

	return monitor.Sighting{
		Observation: policy.Observation{
			Live:        s.Live,
			Status:      status,
			StatusKnown: known && s.Live,
			StatusSince: s.StatusUpdatedAt,
			// The session's own status timestamp, never a timer in this
			// process: a heartbeat here cannot prove the session did anything.
			LastActivityAt: s.StatusUpdatedAt,
			// Which is a transition, not activity. The hook merge upgrades this
			// where an installation is actually reporting the session.
			ActivityResolution: policy.ResolutionTransition,
			ObservedAt:         s.UpdatedAt,

			InputRequestVisibility: visibilityFor(s),
			Evidence:               evidenceFor(s),
			InputRequestKind:       boundDetail(s.WaitingFor),
			Reason:                 claudeReason(s, known),
		},
		// The process start is part of the identity, so a restarted session
		// is a new generation rather than a continuation of the old run.
		SessionIdentity: s.SessionID + "@" + s.ProcStart,
	}
}

// absent decides what a missing registry record means.
//
// On its own it means only that nothing could be read. An exit is claimed only
// where a recorded process identity proves the process is gone — otherwise a
// pruned record, or a registry read at the wrong moment, would be reported as
// a session that finished.
func (c *Claude) absent(target config.Target) (monitor.Sighting, error) {
	pid, procStart := target.Attachment.PID, target.Attachment.ProcStart
	if pid == 0 || procStart == "" {
		return unreadable(policy.ReasonSourceUnreadable), fmt.Errorf(
			"session %s is not in the registry, and no process identity was registered to verify an exit",
			target.Attachment.SessionID)
	}

	sameProcess := c.SameProcess
	if sameProcess == nil {
		sameProcess = proc.SameProcess
	}
	if sameProcess(pid, procStart) {
		// The process is alive but says nothing about itself: a gap in
		// observation, not a finished session.
		return unreadable(policy.ReasonContactLost), fmt.Errorf(
			"session %s has no registry record though pid %d is still running",
			target.Attachment.SessionID, pid)
	}

	return monitor.Sighting{
		Observation: policy.Observation{
			Live:        false,
			Status:      policy.StatusExited,
			StatusKnown: true,
			// Nothing is watching a process that has gone. Not because there
			// is no timestamp here — the value is about the signal, and a
			// process target reports activity with no timestamp at all.
			ActivityResolution: policy.ResolutionUnavailable,
		},
		SessionIdentity: target.Attachment.SessionID + "@" + procStart,
	}, nil
}

// unreadable is the observation of having seen nothing. It asserts no status,
// so it can never overwrite what was last genuinely observed.
func unreadable(reason string) monitor.Sighting {
	return monitor.Sighting{Observation: policy.Observation{
		Live: false, Status: policy.StatusUnknown, StatusKnown: false,
		InputRequestVisibility: policy.VisibilityUnavailable,
		ActivityResolution:     policy.ResolutionUnavailable,
		Reason:                 reason,
	}}
}

func init() {
	provider.Register(provider.Spec{
		Name: "claude", Command: "claude",
		AttachToRunningSession: "yes — session registry, no configuration change and no restart",
		RuntimeState:           "yes — status with its own freshness timestamp",
		ToolEvents:             "with hooks installed (`birddog hooks install`)",
		InputRequests:          "yes — the registry reports a waiting session with no hooks; hooks add which tool is waiting",
		Notes:                  "a status outside busy, idle and waiting is reported as unreadable rather than guessed at",

		AttachToRunningSessionSupport: policy.SupportSupported,
		RuntimeStateSupport:           policy.SupportSupported,
		ToolEventsSupport:             policy.SupportRequiresSetup,
		// Supported rather than requires_setup: the registry reports a
		// waiting session with no hooks at all. Hooks add which tool is
		// waiting, which enriches the answer without carrying it.
		InputRequestsSupport: policy.SupportSupported,
	})
	register("claude", func(d Deps) (monitor.Observer, error) {
		// Wrapped, not replaced: the registry adapter answers first on every
		// pass, and the transcript only adds what it cannot carry.
		return transcript.Watch(NewClaude(d.ClaudeRegistryDir, d.HookStateDir), d.ProjectsDirs), nil
	})
}
