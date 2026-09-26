package observe

import (
	"errors"
	"testing"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/discover/codex"
	"github.com/BrutalSystems/birddog/internal/policy"
)

func codexTarget(threadID string) config.Target {
	return config.Target{
		ID: "worker-1", Provider: "codex",
		Attachment: config.Attachment{Kind: "existing-session", SessionID: threadID},
	}
}

func codexListing(ss ...codex.Session) func() ([]codex.Session, error) {
	return func() ([]codex.Session, error) { return ss, nil }
}

func TestCodexObserverMapsActive(t *testing.T) {
	o := &Codex{List: codexListing(codex.Session{
		ThreadID: "t1", Status: "active", StatusKnown: true, Live: true, HolderPID: 42,
	})}

	got, err := o.Observe(codexTarget("t1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusActive || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want active", got.Status, got.StatusKnown)
	}
}

// A thread whose state could not be read is still a live thread: the writer
// lock proves it runs even when the state database says nothing usable.
func TestCodexUnknownStateIsStillALiveThread(t *testing.T) {
	o := &Codex{List: codexListing(codex.Session{
		ThreadID: "t1", StatusKnown: false, Live: true, HolderPID: 42,
	})}

	got, _ := o.Observe(codexTarget("t1"))
	if !got.Live {
		t.Error("Live = false though a process holds the thread's writer lock")
	}
	if got.StatusKnown {
		t.Error("StatusKnown = true though the state database reported nothing usable")
	}
}

func TestCodexObserverDoesNotGuessAtUnmappedStates(t *testing.T) {
	o := &Codex{List: codexListing(codex.Session{
		ThreadID: "t1", Status: "systemError", StatusKnown: true, Live: true, HolderPID: 42,
	})}

	got, _ := o.Observe(codexTarget("t1"))
	if got.StatusKnown {
		t.Error("StatusKnown = true for a state with no agreed meaning in birddog's vocabulary")
	}
}

// The lock holder is part of the identity: a thread picked up by a different
// process is a different run.
func TestCodexIdentityIncludesTheLockHolder(t *testing.T) {
	first := &Codex{List: codexListing(codex.Session{ThreadID: "t1", Live: true, HolderPID: 1})}
	second := &Codex{List: codexListing(codex.Session{ThreadID: "t1", Live: true, HolderPID: 2})}

	a, _ := first.Observe(codexTarget("t1"))
	b, _ := second.Observe(codexTarget("t1"))
	if a.SessionIdentity == b.SessionIdentity {
		t.Error("a thread taken over by another process kept its identity")
	}
}

// Absence here is positive evidence: the listing is built from held writer
// locks, so a thread missing from it has no process working on it.
func TestCodexThreadWithNoHeldLockIsAnExit(t *testing.T) {
	o := &Codex{List: codexListing()}

	got, err := o.Observe(codexTarget("t1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusExited || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want a verified exit", got.Status, got.StatusKnown)
	}
	if got.Live {
		t.Error("Live = true for a thread nobody holds")
	}
}

// If the lock directory could not be read, absence proves nothing.
func TestCodexListingFailureIsNotAnExit(t *testing.T) {
	o := &Codex{List: func() ([]codex.Session, error) { return nil, errors.New("permission denied") }}

	got, err := o.Observe(codexTarget("t1"))
	if err == nil {
		t.Fatal("Observe = nil error when the thread listing failed")
	}
	if got.Status == policy.StatusExited {
		t.Error("a failed listing was reported as an exit — nothing was verified")
	}
	if got.StatusKnown || got.Live {
		t.Error("a failed listing produced a confident observation")
	}
}

// Codex assigns no activity timestamp on any path, so there is never anything
// to qualify — not a coarse signal, but no signal.
func TestCodexReportsUnavailableResolutionOnEveryPath(t *testing.T) {
	live := &Codex{List: codexListing(codex.Session{
		ThreadID: "t1", Status: "active", StatusKnown: true, Live: true, HolderPID: 42,
	})}
	gone := &Codex{List: codexListing()}

	for _, tc := range []struct {
		name string
		o    *Codex
	}{{"live thread", live}, {"thread missing from the listing", gone}} {
		got, err := tc.o.Observe(codexTarget("t1"))
		if err != nil {
			t.Fatalf("%s: Observe: %v", tc.name, err)
		}
		if got.ActivityResolution != policy.ResolutionUnavailable {
			t.Errorf("%s: ActivityResolution = %q, want unavailable", tc.name, got.ActivityResolution)
		}
	}
}
