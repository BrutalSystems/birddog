package observe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/provider"
)

// Fake observes a session described by a JSON file.
//
// It exists so the whole pipeline — observation, policy, incidents, the event
// feed — can be exercised end to end without a real agent, and so the smoke
// test can drive states that are hard to arrange on demand: an approval wait,
// a long tool run, a disconnect, an exit.
//
// The target's session_id is the absolute path of the state file. A test
// harness writes it; birddog only ever reads it.
type Fake struct{}

// fakeState is what a fake worker publishes about itself.
type fakeState struct {
	Status         string `json:"status"`
	Live           bool   `json:"live"`
	Identity       string `json:"identity"`
	LastActivityAt string `json:"last_activity_at,omitempty"`
	StatusSince    string `json:"status_since,omitempty"`
}

// fakeStatuses is the vocabulary a fake worker may use. It is closed on
// purpose: a typo should fail loudly rather than quietly become "unknown".
var fakeStatuses = map[string]bool{
	policy.StatusActive:       true,
	policy.StatusIdle:         true,
	policy.StatusWaitingInput: true,
	policy.StatusRunningTool:  true,
	policy.StatusExited:       true,
	policy.StatusUnknown:      true,
}

// Observe reads a fake worker's published state.
func (f *Fake) Observe(target config.Target) (monitor.Sighting, error) {
	path := target.Attachment.SessionID
	if !filepath.IsAbs(path) {
		return unreadable(policy.ReasonSourceUnreadable), fmt.Errorf("fake target %q: session_id must be the absolute path of a state file", target.ID)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// A removed file stands for a worker that went away. Nothing is
		// claimed about why: this is a loss of observation, not an exit.
		return unreadable(policy.ReasonSourceUnreadable), fmt.Errorf("read fake state: %w", err)
	}

	var s fakeState
	if err := json.Unmarshal(data, &s); err != nil {
		return unreadable(policy.ReasonSourceUnreadable), fmt.Errorf("fake state %s: %w", path, err)
	}
	if !fakeStatuses[s.Status] {
		return unreadable(policy.ReasonStatusUnrecognised), fmt.Errorf("fake state %s: unknown status %q", path, s.Status)
	}

	activity := parseFakeTime(s.LastActivityAt)
	since := parseFakeTime(s.StatusSince)
	if since.IsZero() {
		since = activity
	}

	// Read from the raw field rather than the parsed value: parseFakeTime
	// defaults to now, so a harness that stated no timestamp is
	// indistinguishable from one that stated this instant after parsing.
	//
	// Which means unavailable here is reported beside a LastActivityAt that
	// advances every pass. That pair is contradictory and deliberate: the
	// timestamp is the parse default rather than a signal, and the resolution
	// is what says so.
	resolution := policy.ResolutionActivity
	if s.LastActivityAt == "" {
		resolution = policy.ResolutionUnavailable
	}

	return monitor.Sighting{
		Observation: policy.Observation{
			Live:               s.Live,
			Status:             s.Status,
			StatusKnown:        s.Status != policy.StatusUnknown,
			StatusSince:        since,
			LastActivityAt:     activity,
			ActivityResolution: resolution,
			ObservedAt:         time.Now().UTC(),
			// The fake worker publishes no permission signal.
			InputRequestVisibility: policy.VisibilityUnavailable,
		},
		SessionIdentity: s.Identity,
	}, nil
}

// parseFakeTime reads an RFC3339 stamp, defaulting to now so a harness need
// not write one for states where it does not matter.
func parseFakeTime(v string) time.Time {
	if v == "" {
		return time.Now().UTC()
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Now().UTC()
	}
	return t.UTC()
}

func init() {
	// The fake provider is how the smoke test drives states that are hard
	// to arrange on demand. It reads a file and nothing else.
	provider.Register(provider.Spec{Name: "fake", Internal: true})
	register("fake", func(Deps) (monitor.Observer, error) { return &Fake{}, nil })
}
