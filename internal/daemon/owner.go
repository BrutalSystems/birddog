package daemon

// An instance outlives the shell that started it, which is what lets
// monitoring survive an orchestrator's turn ending. Nothing reaps it
// afterwards: the daemon detaches into its own session and has no notion of a
// parent, so an orchestrator that closes for good leaves its instance running
// with nobody to read the answers. Strays accumulate silently.
//
// Naming an owner opts into being reaped. It stays opt-in because the
// surviving behaviour is right for a long effort that spans several
// conversations, and wrong only when the watch was for one conversation.

// Owner is the session an instance was started for.
type Owner struct {
	// Description identifies the owner in status output, so an instance that
	// stops can be explained rather than merely noticed.
	Description string

	// Alive reports whether the owner is still there. Injected so the check
	// can be whatever identifies that kind of session.
	Alive func() bool
}

// ownerGrace is how many consecutive absences end the watch.
//
// One missed check is not evidence: a socket may fail to answer under load,
// and a session is briefly unobservable while it restarts. Three in a row,
// at the observation interval, is a session that has gone.
const ownerGrace = 3

// ownerGone reports whether the owner has been absent long enough to act on,
// and tracks the run of absences.
func (d *Daemon) ownerGone() bool {
	if d.opts.Owner == nil || d.opts.Owner.Alive == nil {
		return false
	}

	if d.opts.Owner.Alive() {
		d.ownerMisses = 0
		return false
	}

	d.ownerMisses++
	return d.ownerMisses >= ownerGrace
}
