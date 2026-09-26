package codex

import (
	"errors"
	"sort"
)

// errUnavailable marks metadata that could not be read. Liveness does not
// depend on it, so a listing continues without it rather than failing.
var errUnavailable = errors.New("thread metadata unavailable")

// Session is one live Codex thread.
type Session struct {
	ThreadID string
	Name     string
	CWD      string
	// Status is the thread's own runtime state, empty when it is not known.
	// See StatusKnown: an empty status is never an observation.
	Status               string
	CanAcceptDirectInput bool

	// HolderPID is the process holding the thread's writer lock — the
	// evidence that the thread is live.
	HolderPID int
	Live      bool

	// MetadataAvailable records whether thread state could be read at all.
	// When false, Name and Status are absent because nothing was readable,
	// not because the thread has none.
	MetadataAvailable bool

	// StatusKnown reports whether Status is an observation of this session.
	// False when no metadata was readable, and false for statusNotLoaded,
	// which describes birddog's own app-server rather than the session.
	StatusKnown bool
}

// statusNotLoaded is what the state database reports for a thread that is not
// loaded in the *querying* app-server — which is nearly every live thread,
// since each is owned by somebody else's process. It is birddog's own view
// leaking through, and reporting it as a session state would be inventing one.
const statusNotLoaded = "notLoaded"

// Params configures a listing. Every external dependency is injected so the
// join is testable without codex, locks, or processes.
type Params struct {
	LockDir    string
	LockHolder LockHolder

	// Threads reads thread metadata from the shared state database.
	Threads func() ([]Thread, error)

	// HolderCWD reports the working directory of a lock-holding process.
	HolderCWD func(pid int) (string, bool)
}

// List returns the live Codex threads on this machine.
//
// The writer locks are authoritative, not the state database: the database
// lists threads that ended long ago, while a held lock is the only evidence
// that a thread is running now. Metadata enriches that set; it never defines
// it, and losing it hides no sessions.
func List(p Params) ([]Session, error) {
	live, err := LiveThreads(LockParams{LockDir: p.LockDir, LockHolder: p.LockHolder})
	if err != nil {
		return nil, err
	}

	if len(live) == 0 {
		// Reading metadata means spawning a codex app-server child. With no
		// live thread to describe, that is a process started for nothing.
		return []Session{}, nil
	}

	meta := map[string]Thread{}
	metadataAvailable := true
	if threads, err := p.Threads(); err != nil {
		// The app-server is a separate failure domain from the locks. Losing
		// it costs names and statuses, not the knowledge that a thread runs.
		metadataAvailable = false
	} else {
		for _, t := range threads {
			meta[t.ID] = t
		}
	}

	out := make([]Session, 0, len(live))
	for id, holder := range live {
		s := Session{
			ThreadID:          id,
			HolderPID:         holder.PID,
			Live:              true,
			MetadataAvailable: metadataAvailable,
		}
		if t, ok := meta[id]; ok {
			s.Name = t.Name
			s.CWD = t.CWD
			s.CanAcceptDirectInput = t.CanAcceptDirectInput
			if t.Status != "" && t.Status != statusNotLoaded {
				s.Status = t.Status
				s.StatusKnown = true
			}
		}
		if s.CWD == "" {
			// A thread that has not taken a turn yet has no state-DB record.
			// The holder's working directory is the only thing naming it.
			if cwd, ok := p.HolderCWD(holder.PID); ok {
				s.CWD = cwd
			}
		}
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ThreadID < out[j].ThreadID })
	return out, nil
}
