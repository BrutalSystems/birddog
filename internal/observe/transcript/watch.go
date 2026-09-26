// internal/observe/transcript/watch.go
package transcript

import (
	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
)

// Watcher adds transcript evidence to what another observer saw.
//
// The inner observer runs first and unmodified on every pass, so the
// structured answer is never displaced by the read one. Nothing here revives
// a session the inner observer reported gone, and nothing overwrites an input
// request it already reported: a permission prompt from the harness's own
// record outranks a question read out of a conversation.
type Watcher struct {
	Inner monitor.Observer

	// ProjectsDirs is every candidate transcripts directory, searched in
	// order. See Locate.
	ProjectsDirs []string

	// Window is how much of the transcript tail is read. Zero means
	// DefaultWindow.
	Window int64
}

// Watch wraps an observer with transcript enrichment.
func Watch(inner monitor.Observer, projectsDirs []string) *Watcher {
	return &Watcher{Inner: inner, ProjectsDirs: projectsDirs}
}

// Observe reports the inner observer's sighting, enriched where the target
// asked for it and the transcript had something to say.
//
// It never returns an error for an enrichment failure. The monitor discards a
// sighting that arrives with one, so reporting a transcript it could not read
// that way would throw away the inner observer's good answer and open an
// observation_lost on a session that is running fine. The failure is carried
// in InputRequestVisibility instead, which is the field that exists to say
// birddog could not look.
func (w *Watcher) Observe(target config.Target) (monitor.Sighting, error) {
	s, err := w.Inner.Observe(target)
	if err != nil {
		return s, err
	}
	if !target.Observations.Transcript || !s.Live {
		return s, nil
	}
	// An input request already reported came from a stronger source.
	if s.InputRequestVisibility == policy.VisibilityObserved {
		return s, nil
	}

	// Nothing below reports a request, so any evidence the inner observer
	// stamped describes a request that is no longer being made. Sourcing one
	// that is not reported is the opposite of what the field is for.
	s.Evidence = ""

	// Reason is deliberately not set anywhere below: it says why the
	// observation is not a current, readable answer, and it is one — the
	// registry answered. Only the enrichment failed.
	path, locErr := Locate(w.ProjectsDirs, target.Attachment.SessionID)
	if locErr != nil || path == "" {
		s.InputRequestVisibility = policy.VisibilityUnavailable
		return s, nil
	}

	window := w.Window
	if window == 0 {
		window = DefaultWindow
	}
	recs, reachedStart, readErr := Tail(path, window)
	if readErr != nil {
		s.InputRequestVisibility = policy.VisibilityUnavailable
		return s, nil
	}
	if len(recs) == 0 && !reachedStart {
		// The window held no message record and did not reach the start of
		// the file, so birddog never got far enough back to look. Reporting
		// not_observed here would claim it looked and saw none.
		s.InputRequestVisibility = policy.VisibilityUnavailable
		return s, nil
	}

	q := Detect(recs, s.Status == policy.StatusIdle)
	if q == nil {
		// Read cleanly and found nothing. That is not evidence the session
		// is unblocked, only that birddog looked.
		s.InputRequestVisibility = policy.VisibilityNotObserved
		return s, nil
	}

	s.Status = policy.StatusWaitingInput
	s.StatusKnown = true
	s.InputRequestVisibility = policy.VisibilityObserved
	s.InputRequestKind = q.Kind
	// Bounded like every other path into the durable record: this one reads
	// free-form prose, so it is the widest.
	s.InputRequestDetail = policy.BoundDetail(q.Detail)
	s.Evidence = policy.EvidenceTranscript
	return s, nil
}
