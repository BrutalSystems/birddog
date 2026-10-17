package observe

import (
	"errors"
	"testing"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/policy"
)

// A session missing from a listing that could not read every registry may be
// in the one that failed. Nothing is concluded, and above all no exit.
func TestAPartialListingNeverReportsAnExit(t *testing.T) {
	partial := &claude.PartialError{Unreadable: map[string]error{"/r": errors.New("permission denied")}}
	o := &Claude{
		List:        func() ([]claude.Session, error) { return nil, partial },
		SameProcess: func(int, string) bool { return false }, // would be an exit
	}

	got, err := o.Observe(attachedTarget("s1", 100, "P"))
	if err == nil {
		t.Error("Observe = nil error from a partial listing")
	}
	if got.StatusKnown || got.Status == policy.StatusExited {
		t.Errorf("sighting = %+v, want nothing asserted", got.Observation)
	}
	if got.Reason != policy.ReasonSourceUnreadable {
		t.Errorf("Reason = %q, want source_unreadable", got.Reason)
	}
}

// What a partial listing did find is still observed.
func TestAPartialListingStillObservesWhatItFound(t *testing.T) {
	partial := &claude.PartialError{Unreadable: map[string]error{"/r": errors.New("permission denied")}}
	o := &Claude{List: func() ([]claude.Session, error) {
		return []claude.Session{{SessionID: "s1", Status: "idle", Live: true}}, partial
	}}
	got, err := o.Observe(claudeTarget("s1"))
	if err != nil || !got.Live || got.Status != policy.StatusIdle {
		t.Errorf("Observe = %+v, %v; want the session it found", got.Observation, err)
	}
}
