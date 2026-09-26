// Package instrument reports whether birddog's own observation machinery is
// in place on this machine.
//
// A provider capability says what could be observed. This says whether the
// setup that would make it so has actually been done here — the half a
// consumer could not see, because `requires_setup` reads the same whether the
// setup is in place or missing.
//
// Nothing here is a judgement about the operator. Not installing is a choice,
// and only a thing that is installed and cannot work is reported as a fault.
package instrument

import "time"

// Report is the state of one instrumentation mechanism.
type Report struct {
	// Mechanism names the thing, and Provider the runtime it instruments, so
	// a consumer can join this to the capability that says requires_setup.
	Mechanism string
	Provider  string

	// State is one of the policy.Instrument* values.
	State string

	// Detail is the operator's sentence. State and Detail are the same claim
	// said twice, the way a capability states support and prose together.
	Detail string

	// Version is the version last seen running — taken from a session that
	// is publishing now where there is one, and otherwise from the last time
	// the instrumentation recorded starting. It is not a claim about what is
	// installed on disk, which may be newer than anything that has run.
	Version string

	// Problem is set only when something installed cannot work. Empty for
	// not_installed and unknown, which are answers rather than faults.
	Problem string

	// Events and Missing are the lifecycle events a hook install covers and
	// does not. Empty for mechanisms that are not event-based.
	Events  []string
	Missing []string

	// PublishingSessions counts sessions currently publishing. Zero is not a
	// fault: it usually means nothing is running.
	PublishingSessions int
}

// Opencode locates the birddog opencode plugin's two traces on disk.
type Opencode struct {
	// RecordsDir holds one record per publishing session.
	RecordsDir string

	// LogPath is the plugin's diagnostic log, beside the records.
	LogPath string

	// StaleAfter bounds how long a record is taken as evidence that the
	// session behind it is still publishing.
	StaleAfter time.Duration

	// Now is injected so staleness is testable.
	Now func() time.Time
}
