package claude

// Tracker follows one session across a new session id in the same process.
//
// Claude Code keeps one registry record per process and rewrites its session
// id in place on /clear. Anything that names a session by id — the owner an
// instance is tied to, the orchestrator alerts are delivered to — would
// otherwise lose it at the first /clear while the process it meant is still
// there. The link is the process identity, pid and start time together,
// learned from the listings that found the session under its id; a pid alone
// is reused by the kernel and is never enough.
//
// Not safe for concurrent use.
type Tracker struct {
	// SessionID is the id being tracked. Updated when the session is found
	// under a new one.
	SessionID string

	pid       int
	procStart string
}

// Find returns the tracked session in a listing: by its id, or else by the
// process last seen holding that id when the record at that process now
// carries a different one. A successor is only taken when it is live, so a
// record left behind by something that has gone cannot be adopted.
func (t *Tracker) Find(sessions []Session) (Session, bool) {
	for _, s := range sessions {
		if s.SessionID != t.SessionID {
			continue
		}
		if s.PID != 0 && s.ProcStart != "" {
			t.pid, t.procStart = s.PID, s.ProcStart
		}
		return s, true
	}
	if t.pid == 0 || t.procStart == "" {
		return Session{}, false
	}
	for _, s := range sessions {
		if s.PID == t.pid && s.ProcStart == t.procStart && s.Live && s.SessionID != "" {
			t.SessionID = s.SessionID
			return s, true
		}
	}
	return Session{}, false
}
