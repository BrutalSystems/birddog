package observe

import (
	"fmt"
	"strconv"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/discover/codex"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/provider"
)

// Codex observes Codex threads.
//
// Liveness comes from the writer locks, which need no codex process at all;
// state comes from an app-server child spawned only when there is a live
// thread to describe.
type Codex struct {
	List func() ([]codex.Session, error)
}

// NewCodex builds an observer over the real thread locks and state database.
func NewCodex(lockDir string) *Codex {
	return &Codex{
		List: func() ([]codex.Session, error) {
			return codex.List(codex.Params{
				LockDir:    lockDir,
				LockHolder: codex.LsofHolder,
				HolderCWD:  codex.HolderCWD,
				Threads: func() ([]codex.Thread, error) {
					client, err := codex.Connect()
					if err != nil {
						return nil, err
					}
					defer client.Close()
					return codex.ListThreads(client)
				},
			})
		},
	}
}

// codexStatusMap is the part of Codex's thread vocabulary with an agreed
// meaning here. `notLoaded` never reaches this map — discovery already drops
// it, because it describes the querying app-server rather than the session.
var codexStatusMap = map[string]string{
	"active": policy.StatusActive,
	"idle":   policy.StatusIdle,
}

// codexReason says why an observation is not a current, readable answer.
//
// A live thread whose status cannot be read is the ordinary case rather than a
// fault: the state database reports a thread as notLoaded to anyone who does
// not own it, which describes the asking process rather than the session. That
// is a limit of what Codex exposes to an outside observer, so it is reported
// as one — not as a thread that failed to answer.
func codexReason(live, statusKnown bool) string {
	switch {
	case !live:
		return policy.ReasonContactLost
	case !statusKnown:
		return policy.ReasonProviderLimited
	default:
		return ""
	}
}

// Observe reports the current state of one Codex thread.
func (c *Codex) Observe(target config.Target) (monitor.Sighting, error) {
	threads, err := c.List()
	if err != nil {
		// Without a readable listing, absence proves nothing at all.
		return unreadable(policy.ReasonSourceUnreadable), fmt.Errorf("list live Codex threads: %w", err)
	}

	for _, s := range threads {
		if s.ThreadID != target.Attachment.SessionID {
			continue
		}
		status, known := codexStatusMap[s.Status]
		if !known {
			status = policy.StatusUnknown
		}
		return monitor.Sighting{
			Observation: policy.Observation{
				Live:        s.Live,
				Status:      status,
				StatusKnown: known && s.StatusKnown,
				// Codex exposes no input-wait signal to an outside observer.
				InputRequestVisibility: policy.VisibilityUnavailable,
				// And no activity timestamp of any kind, on any path.
				ActivityResolution: policy.ResolutionUnavailable,
				Reason:             codexReason(s.Live, known && s.StatusKnown),
			},
			// The holder is part of the identity: a thread picked up by a
			// different process is a different run.
			SessionIdentity: s.ThreadID + "@" + strconv.Itoa(s.HolderPID),
		}, nil
	}

	// The listing is built from held writer locks, so a thread missing from a
	// listing that succeeded has no process working on it. That is evidence,
	// not inference.
	return monitor.Sighting{
		Observation: policy.Observation{
			Live:               false,
			Status:             policy.StatusExited,
			StatusKnown:        true,
			ActivityResolution: policy.ResolutionUnavailable,
		},
		SessionIdentity: target.Attachment.SessionID,
	}, nil
}

func init() {
	provider.Register(provider.Spec{
		Name: "codex", Command: "codex",
		AttachToRunningSession: "partial — liveness from writer locks; state needs an app-server child of our own",
		RuntimeState:           "mostly unavailable — threads owned elsewhere report notLoaded, which describes the caller",
		ToolEvents:             "unavailable",
		InputRequests:          "unavailable",
		Notes:                  "the only IPC socket under ~/.codex belongs to the desktop app, which is out of scope",

		// Partial because the field bundles two answers that differ:
		// liveness is observable from the writer locks, state is not.
		// Splitting the field would say it better, but that would change a
		// field a consumer relies on. See issue #14.
		AttachToRunningSessionSupport: policy.SupportPartial,
		RuntimeStateSupport:           policy.SupportProviderLimited,
		ToolEventsSupport:             policy.SupportProviderLimited,
		InputRequestsSupport:          policy.SupportProviderLimited,
	})
	register("codex", func(d Deps) (monitor.Observer, error) {
		return NewCodex(d.CodexLockDir), nil
	})
}
